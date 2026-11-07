package tart

import (
	"context"
	"sync"
)

// limiter bounds how many VMs one act process runs at once. Apple's
// license and the Virtualization framework allow two macOS VMs per host.
type limiter struct {
	slots chan struct{}
}

func newLimiter(n int) *limiter {
	if n <= 0 {
		n = DefaultMaxVMs
	}
	return &limiter{slots: make(chan struct{}, n)}
}

var (
	limitersMu sync.Mutex
	limiters   = map[int]*limiter{}
)

// sharedLimiter returns the process-wide limiter for n slots.
func sharedLimiter(n int) *limiter {
	if n <= 0 {
		n = DefaultMaxVMs
	}
	limitersMu.Lock()
	defer limitersMu.Unlock()
	l, ok := limiters[n]
	if !ok {
		l = newLimiter(n)
		limiters[n] = l
	}
	return l
}

// slot is one acquired VM slot.
type slot struct {
	once sync.Once
	l    *limiter
}

// Acquire waits for a free slot or for ctx to end.
func (l *limiter) Acquire(ctx context.Context) (*slot, error) {
	select {
	case l.slots <- struct{}{}:
		return &slot{l: l}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Release frees the slot. Calling it more than once is harmless.
func (s *slot) Release() {
	if s == nil {
		return
	}
	s.once.Do(func() { <-s.l.slots })
}
