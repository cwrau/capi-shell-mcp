package proxy

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

// systemdManager is a Manager under systemd whose unit is active (or not) as
// given, with readiness checking turned on and no real waiting.
func systemdManager(t *testing.T, unitActive bool, probe ProbeFunc) (*Manager, *scriptedExecer, *[]time.Duration) {
	t.Helper()
	execer := &scriptedExecer{handler: func(file string, args []string) (shell.Output, error) {
		if file == "systemctl" && args[1] == "is-active" {
			if unitActive {
				return shell.Output{Stdout: "active\n"}, nil
			}
			return shell.Output{Stdout: "inactive\n"}, &shell.ExitError{Stdout: "inactive\n", ExitCode: 3, Err: errAnyNonZero}
		}
		return shell.Output{}, nil
	}}
	m := New(&fakeClock{}, execer, (&fakeSpawner{}).spawn, underSystemd)
	m.SetReadiness(probe, 20*time.Second)
	var slept []time.Duration
	m.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return m, execer, &slept
}

// systemd-run returns as soon as the unit is started, but sshuttle needs a
// while to connect and set up its redirect: until then the API server cannot
// be reached through the tunnel. EnsureProxy must not report success before
// it can.
func TestEnsureProxyWaitsUntilTheAPIServerAnswersThroughANewTunnel(t *testing.T) {
	var probes atomic.Int32
	m, _, slept := systemdManager(t, false, func(_ context.Context, addr string) error {
		if addr != "10.1.2.3:6443" {
			t.Errorf("probed %q, want the API server address", addr)
		}
		if probes.Add(1) < 4 {
			return errors.New("i/o timeout") // sshuttle has not set up its redirect yet
		}
		return nil
	})

	if err := m.EnsureProxy(context.Background(), "10.1.2.3", "6443", "user@bastion", time.Minute); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}
	if got := probes.Load(); got != 4 {
		t.Errorf("probed %d times, want 4 (three failures, then success)", got)
	}
	if len(*slept) != 3 {
		t.Errorf("waited %d times between probes, want 3", len(*slept))
	}
}

// A unit that is "active" may only just have started: is-active says so while
// sshuttle is still connecting. Reusing it is subject to the same check.
func TestEnsureProxyChecksAnAlreadyActiveTunnelToo(t *testing.T) {
	var probes atomic.Int32
	m, execer, _ := systemdManager(t, true, func(context.Context, string) error {
		if probes.Add(1) < 2 {
			return errors.New("i/o timeout")
		}
		return nil
	})

	if err := m.EnsureProxy(context.Background(), "10.1.2.3", "6443", "user@bastion", time.Minute); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}
	if probes.Load() != 2 {
		t.Errorf("probed %d times, want 2", probes.Load())
	}
	for _, c := range execer.calls {
		if c.file == "systemd-run" {
			t.Errorf("a new unit was started although one was active: %v", c.args)
		}
	}
}

func TestEnsureProxyDoesNotWaitWhenTheAPIServerAlreadyAnswers(t *testing.T) {
	m, _, slept := systemdManager(t, true, func(context.Context, string) error { return nil })

	if err := m.EnsureProxy(context.Background(), "10.1.2.3", "6443", "user@bastion", time.Minute); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}
	if len(*slept) != 0 {
		t.Errorf("waited %d times for a tunnel that was already working", len(*slept))
	}
}

func TestEnsureProxyFailsWithAClearErrorWhenTheTunnelNeverWorks(t *testing.T) {
	m, _, _ := systemdManager(t, false, func(context.Context, string) error { return errors.New("dial tcp: i/o timeout") })
	// A short real deadline: the fake sleep returns at once, so the loop runs
	// until the deadline passes.
	m.SetReadiness(func(context.Context, string) error { return errors.New("dial tcp: i/o timeout") }, 50*time.Millisecond)

	err := m.EnsureProxy(context.Background(), "10.1.2.3", "6443", "user@bastion", time.Minute)
	if err == nil {
		t.Fatal("EnsureProxy succeeded for a tunnel that never worked")
	}
	for _, want := range []string{"10.1.2.3:6443", "tunnel", "i/o timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestEnsureProxyStopsWaitingWhenTheCallerGivesUp(t *testing.T) {
	m, _, _ := systemdManager(t, false, func(context.Context, string) error { return errors.New("i/o timeout") })
	m.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := m.EnsureProxy(ctx, "10.1.2.3", "6443", "user@bastion", time.Minute); err == nil {
		t.Fatal("EnsureProxy succeeded with a cancelled context and a tunnel that does not work")
	}
}

// Without SetReadiness nothing is probed, as before.
func TestEnsureProxyWithoutReadinessCheckingBehavesAsBefore(t *testing.T) {
	execer := &scriptedExecer{handler: func(file string, args []string) (shell.Output, error) {
		return shell.Output{Stdout: "active\n"}, nil
	}}
	m := New(&fakeClock{}, execer, (&fakeSpawner{}).spawn, underSystemd)

	if err := m.EnsureProxy(context.Background(), "10.1.2.3", "6443", "user@bastion", time.Minute); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}
}

// Many callers for one target share one wait, not one each.
func TestConcurrentEnsureProxyCallsShareOneReadinessWait(t *testing.T) {
	var probes atomic.Int32
	release := make(chan struct{})
	m, _, _ := systemdManager(t, true, func(context.Context, string) error {
		probes.Add(1)
		<-release
		return nil
	})

	errs := make(chan error, 8)
	for range 8 {
		go func() { errs <- m.EnsureProxy(context.Background(), "10.1.2.3", "6443", "user@bastion", time.Minute) }()
	}
	// All eight are inside the shared attempt (or queued behind it) once the
	// first probe is running; letting it finish releases them together.
	for probes.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatalf("EnsureProxy: %v", err)
		}
	}
	if got := probes.Load(); got > 2 {
		t.Errorf("%d probes for 8 concurrent callers, want them to share (at most 2 rounds)", got)
	}
}
