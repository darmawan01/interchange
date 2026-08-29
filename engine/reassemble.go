package engine

import (
	"sync"
	"time"

	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
)

type partial struct {
	chunks   map[uint64][]byte
	total    uint64 // sequence of the KIND_END frame; 0 means not seen yet
	haveEnd  bool
	size     int
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

// accept feeds one frame in. It returns the reassembled body once the message
// is complete, or nil while it is still incomplete.
func (r *reassembler) accept(f *transportv1.Frame) ([]byte, error) {
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
	case transportv1.Frame_KIND_MESSAGE:
		if _, dup := p.chunks[f.GetSequence()]; dup {
			// Replay. The receiver never sees it twice.
			return nil, nil
		}
		p.size += len(f.GetPayload())
		if r.max > 0 && p.size > r.max {
			delete(r.m, f.GetCorrelationId())
			return nil, errTooLarge{size: p.size, max: r.max}
		}
		p.chunks[f.GetSequence()] = f.GetPayload()
	default:
		// KIND_ERROR and KIND_UNSPECIFIED carry no body to reassemble.
		delete(r.m, f.GetCorrelationId())
		return nil, nil
	}

	if !p.haveEnd || uint64(len(p.chunks)) != p.total {
		return nil, nil
	}
	out := make([]byte, 0, p.size)
	for i := uint64(0); i < p.total; i++ {
		c, ok := p.chunks[i]
		if !ok {
			// A gap with the end already seen: wait for the redelivery.
			return nil, nil
		}
		out = append(out, c...)
	}
	delete(r.m, f.GetCorrelationId())
	return out, nil
}

func (r *reassembler) sweepLocked() {
	now := r.now()
	for k, p := range r.m {
		if now.After(p.deadline) {
			delete(r.m, k)
		}
	}
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
