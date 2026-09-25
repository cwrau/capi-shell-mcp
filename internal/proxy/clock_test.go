package proxy

import (
	"sync"
	"time"
)

// fakeClock is a test double for Clock: AfterFunc never actually waits —
// each call is recorded as a fakeTimer that the test fires explicitly,
// mirroring vitest's vi.advanceTimersByTime without a real event loop.
type fakeClock struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	mu      sync.Mutex
	fn      func()
	stopped bool
	fired   bool
}

func (c *fakeClock) AfterFunc(_ time.Duration, f func()) Timer {
	t := &fakeTimer{fn: f}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	return t
}

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

// Fire invokes the timer's callback as if its deadline elapsed, unless it
// was stopped first.
func (t *fakeTimer) Fire() {
	t.mu.Lock()
	if t.stopped || t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	fn := t.fn
	t.mu.Unlock()
	fn()
}
