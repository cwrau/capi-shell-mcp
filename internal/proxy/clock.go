package proxy

import "time"

// Clock schedules delayed callbacks. The production implementation
// (RealClock) wraps time.AfterFunc; tests substitute a fake so TTL/timeout
// behavior can be driven deterministically without real sleeps.
type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
}

type Timer interface {
	Stop() bool
}

// RealClock is the production Clock, backed by time.AfterFunc.
type RealClock struct{}

func (RealClock) AfterFunc(d time.Duration, f func()) Timer {
	return realTimer{time.AfterFunc(d, f)}
}

type realTimer struct{ t *time.Timer }

func (r realTimer) Stop() bool { return r.t.Stop() }
