package engine

import (
	"sync"
	"time"
)

// dedupe is server-side replay suppression for whole messages. It is enabled
// only when a driver reports AtLeastOnce: on an exactly-once-ish transport it
// costs nothing because it is never constructed.
//
// A redelivered request replays the cached response rather than the handler.
// Dropping the message outright would be simpler and wrong -- the redelivery
// usually happened *because* the first response was lost.
type dedupe struct {
	mu  sync.Mutex
	m   map[string]*dedupeEntry
	ttl time.Duration
	now func() time.Time
}

type dedupeEntry struct {
	done     chan struct{}
	response []byte
	expires  time.Time
}

func newDedupe(ttl time.Duration) *dedupe {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &dedupe{m: map[string]*dedupeEntry{}, ttl: ttl, now: time.Now}
}

// begin claims a correlation id. It returns claimed=true for the first
// arrival; a later arrival gets claimed=false and the first call's response
// once it is available.
func (d *dedupe) begin(id string) (entry *dedupeEntry, claimed bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked()
	if e, ok := d.m[id]; ok {
		return e, false
	}
	e := &dedupeEntry{done: make(chan struct{}), expires: d.now().Add(d.ttl)}
	d.m[id] = e
	return e, true
}

// complete records the response bytes for a claimed id and releases anyone
// waiting on the replay path.
func (d *dedupe) complete(e *dedupeEntry, response []byte) {
	d.mu.Lock()
	e.response = response
	d.mu.Unlock()
	close(e.done)
}

// abandon releases a claim that produced no response, so a redelivery gets a
// fresh attempt rather than waiting forever on a call that never finished.
func (d *dedupe) abandon(id string, e *dedupeEntry) {
	d.mu.Lock()
	if cur, ok := d.m[id]; ok && cur == e {
		delete(d.m, id)
	}
	d.mu.Unlock()
	close(e.done)
}

func (e *dedupeEntry) wait(timeout time.Duration) ([]byte, bool) {
	select {
	case <-e.done:
		return e.response, e.response != nil
	case <-time.After(timeout):
		return nil, false
	}
}

func (d *dedupe) sweepLocked() {
	now := d.now()
	for k, e := range d.m {
		if now.After(e.expires) {
			delete(d.m, k)
		}
	}
}
