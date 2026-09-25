package proxy

import (
	"testing"
	"time"
)

func TestRealClockAfterFuncFiresCallback(t *testing.T) {
	fired := make(chan struct{}, 1)
	timer := (RealClock{}).AfterFunc(10*time.Millisecond, func() { fired <- struct{}{} })

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("callback did not fire within 1s")
	}

	if timer.Stop() {
		t.Error("Stop() = true after the timer already fired, want false")
	}
}

func TestRealClockAfterFuncStopPreventsCallback(t *testing.T) {
	fired := make(chan struct{}, 1)
	timer := (RealClock{}).AfterFunc(50*time.Millisecond, func() { fired <- struct{}{} })

	if !timer.Stop() {
		t.Fatal("Stop() = false, want true (timer had not yet fired)")
	}

	select {
	case <-fired:
		t.Fatal("callback fired despite Stop()")
	case <-time.After(100 * time.Millisecond):
	}
}
