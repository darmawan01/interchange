package engine

import (
	"sync"
	"time"

	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
)

type partial struct {
	chunks  map[uint64][]byte
	total   uint64 // sequence of the KIND_END frame; 0 means not seen yet
	haveEnd bool
	size    int

	// acks are the per-frame Done callbacks, held until the whole message is
	// handled. Acking a chunk on arrival would tell the broker a message was
	// processed while it is still half a message sitting in memory.
	acks     []func(error)
	deadline time.Time
}

// reassembler collects chunked frames back into whole messages. It is shared
// by both directions: a large request and a large response are the same
// problem.
//
// It also implements replay suppression: on an at-least-once transport a
// frame can arrive twice, and a duplicate sequence is dropped rather than
// appended.
type reassembler struct {
	mu  sync.Mutex
	m   map[string]*partial
	ttl time.Duration
	max int // per-message ceiling; 0 means unlimited
	now func() time.Time
}

func newReassembler(ttl time.Duration, max int) *reassembler {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &reassembler{m: map[string]*partial{}, ttl: ttl, max: max, now: time.Now}
}

// accept feeds one frame in, with the transport's acknowledgement callback
// for that frame. It returns the reassembled body once the message is
// complete, together with every held acknowledgement, or a nil body while the
// message is still incomplete.
func (r *reassembler) accept(f *transportv1.Frame, done func(error)) ([]byte, []func(error), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()

	p := r.m[f.GetCorrelationId()]
	if p == nil {
		p = &partial{chunks: map[uint64][]byte{}, deadline: r.now().Add(r.ttl)}
		r.m[f.GetCorrelationId()] = p
	}

	switch f.GetKind() {
	case transportv1.Frame_KIND_END:
		p.haveEnd = true
		p.total = f.GetSequence()
		p.hold(done)
	case transportv1.Frame_KIND_MESSAGE:
		if _, dup := p.chunks[f.GetSequence()]; dup {
			// Replay. The receiver never sees it twice, and the duplicate is
			// acknowledged immediately: it is already accounted for.
			return nil, ack(done), nil
		}
		p.size += len(f.GetPayload())
		if r.max > 0 && p.size > r.max {
			delete(r.m, f.GetCorrelationId())
			return nil, append(p.acks, done), errTooLarge{size: p.size, max: r.max}
		}
		p.chunks[f.GetSequence()] = f.GetPayload()
		p.hold(done)
	default:
		// KIND_ERROR and KIND_UNSPECIFIED carry no body to reassemble.
		delete(r.m, f.GetCorrelationId())
		return nil, append(p.acks, done), nil
	}

	if !p.haveEnd || uint64(len(p.chunks)) != p.total {
		return nil, nil, nil
	}
	out := make([]byte, 0, p.size)
	for i := uint64(0); i < p.total; i++ {
		if _, ok := p.chunks[i]; !ok {
			// A gap with the end already seen: wait for the redelivery.
			return nil, nil, nil
		}
	}
	for i := uint64(0); i < p.total; i++ {
		out = append(out, p.chunks[i]...)
	}
	delete(r.m, f.GetCorrelationId())
	return out, p.acks, nil
}

func (p *partial) hold(done func(error)) {
	if done != nil {
		p.acks = append(p.acks, done)
	}
}

func ack(done func(error)) []func(error) {
	if done == nil {
		return nil
	}
	return []func(error){done}
}

func (r *reassembler) sweepLocked() {
	now := r.now()
	for k, p := range r.m {
		if !now.After(p.deadline) {
			continue
		}
		delete(r.m, k)
		// A message that never completed was never handled. Say so, so a
		// transport that can redeliver does.
		for _, done := range p.acks {
			done(errIncomplete{correlationID: k})
		}
	}
}

type errIncomplete struct{ correlationID string }

func (e errIncomplete) Error() string {
	return "engine: chunked message " + e.correlationID + " expired before every frame arrived"
}

func (r *reassembler) pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.m)
}

type errTooLarge struct {
	size, max int
}

func (e errTooLarge) Error() string {
	return "engine: reassembled message exceeds the configured ceiling"
}
