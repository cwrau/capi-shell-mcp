package proxy

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

// fakeSpawner records spawn calls and returns a real short-lived or
// long-lived child process so Manager's exit-detection goroutine has a
// real process to Wait() on.
type fakeSpawner struct {
	mu    sync.Mutex
	calls [][3]string // host, ip, port
	// exitImmediately makes the spawned process exit right away, simulating
	// sshuttle dying during the startup window.
	exitImmediately bool
}

func (f *fakeSpawner) spawn(host, ip, port string) (*exec.Cmd, error) {
	f.mu.Lock()
	f.calls = append(f.calls, [3]string{host, ip, port})
	f.mu.Unlock()

	var cmd *exec.Cmd
	if f.exitImmediately {
		cmd = exec.Command("sh", "-c", "exit 1")
	} else {
		cmd = exec.Command("sh", "-c", "sleep 5")
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (f *fakeSpawner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func notUnderSystemd() bool { return false }
func underSystemd() bool    { return true }

func (m *Manager) isGone(apiServerIP, apiServerPort string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.processEntries[targetKey(apiServerIP, apiServerPort)]
	return !ok
}

func newTestManager(spawner *fakeSpawner) (*Manager, *fakeClock) {
	clock := &fakeClock{}
	m := New(clock, shell.OSExecer{}, spawner.spawn, notUnderSystemd)
	return m, clock
}

func TestEnsureProxyStartsSshuttleAndStoresEntry(t *testing.T) {
	spawner := &fakeSpawner{}
	m, clock := newTestManager(spawner)

	done := make(chan error, 1)
	go func() {
		done <- m.EnsureProxy(context.Background(), "10.0.0.1", "6443", "user@bastion", 60*time.Second)
	}()

	waitForTimers(t, clock, 1)
	clock.timers[0].Fire() // fires the 1s startup window

	if err := <-done; err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}
	if spawner.callCount() != 1 {
		t.Fatalf("spawn called %d times, want 1", spawner.callCount())
	}
	if spawner.calls[0] != [3]string{"user@bastion", "10.0.0.1", "6443"} {
		t.Errorf("spawn args = %+v", spawner.calls[0])
	}
}

func TestEnsureProxyRefreshesInsteadOfRespawningOnRepeatedCall(t *testing.T) {
	spawner := &fakeSpawner{}
	m, clock := newTestManager(spawner)

	done := make(chan error, 1)
	go func() {
		done <- m.EnsureProxy(context.Background(), "10.0.1.1", "6443", "user@bastion", 60*time.Second)
	}()
	waitForTimers(t, clock, 1)
	clock.timers[0].Fire()
	if err := <-done; err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}

	if err := m.EnsureProxy(context.Background(), "10.0.1.1", "6443", "user@bastion", 60*time.Second); err != nil {
		t.Fatalf("second EnsureProxy: %v", err)
	}
	if spawner.callCount() != 1 {
		t.Errorf("spawn called %d times, want 1 (second call should refresh, not respawn)", spawner.callCount())
	}
}

func TestEnsureProxyKillsAfterTTLExpires(t *testing.T) {
	spawner := &fakeSpawner{}
	m, clock := newTestManager(spawner)

	done := make(chan error, 1)
	go func() {
		done <- m.EnsureProxy(context.Background(), "10.0.2.1", "6443", "user@bastion", 60*time.Second)
	}()
	waitForTimers(t, clock, 1)
	clock.timers[0].Fire()
	if err := <-done; err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}

	waitForTimers(t, clock, 2)
	clock.timers[1].Fire() // the TTL kill timer

	if !m.isGone("10.0.2.1", "6443") {
		t.Error("entry still present after TTL timer fired")
	}
}

func TestEnsureProxyExtendsTTLOnRepeatedCallInsteadOfKillingAtOriginalDeadline(t *testing.T) {
	spawner := &fakeSpawner{}
	m, clock := newTestManager(spawner)

	done := make(chan error, 1)
	go func() {
		done <- m.EnsureProxy(context.Background(), "10.0.6.1", "6443", "user@bastion", 60*time.Second)
	}()
	waitForTimers(t, clock, 1)
	clock.timers[0].Fire()
	if err := <-done; err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}

	if err := m.EnsureProxy(context.Background(), "10.0.6.1", "6443", "user@bastion", 60*time.Second); err != nil {
		t.Fatalf("second EnsureProxy: %v", err)
	}

	// The original kill timer (timers[1]) must have been stopped by the
	// refresh — firing it now must not remove the entry. Only the new timer
	// registered by the refresh (timers[2]) should.
	waitForTimers(t, clock, 3)
	clock.timers[1].Fire()
	if m.isGone("10.0.6.1", "6443") {
		t.Fatal("entry removed by the original (should-be-stopped) timer")
	}

	clock.timers[2].Fire()
	if !m.isGone("10.0.6.1", "6443") {
		t.Error("entry still present after the refreshed timer fired")
	}
}

func TestEnsureProxyRejectsWhenSshuttleExitsDuringStartupWindow(t *testing.T) {
	spawner := &fakeSpawner{exitImmediately: true}
	m, clock := newTestManager(spawner)

	done := make(chan error, 1)
	go func() {
		done <- m.EnsureProxy(context.Background(), "10.0.3.1", "6443", "user@bastion", 60*time.Second)
	}()

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "exited early") {
		t.Fatalf("err = %v, want mention of 'exited early'", err)
	}
	_ = clock
}

func TestEnsureProxySpawnsOnceForConcurrentCallsOnSameTarget(t *testing.T) {
	spawner := &fakeSpawner{}
	m, clock := newTestManager(spawner)

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() {
		done1 <- m.EnsureProxy(context.Background(), "10.0.4.1", "6443", "user@bastion", 60*time.Second)
	}()
	go func() {
		done2 <- m.EnsureProxy(context.Background(), "10.0.4.1", "6443", "user@bastion", 60*time.Second)
	}()

	waitForTimers(t, clock, 1)
	clock.timers[0].Fire()

	if err := <-done1; err != nil {
		t.Fatalf("EnsureProxy #1: %v", err)
	}
	if err := <-done2; err != nil {
		t.Fatalf("EnsureProxy #2: %v", err)
	}
	if spawner.callCount() != 1 {
		t.Errorf("spawn called %d times, want 1", spawner.callCount())
	}
}

func TestEnsureProxySharesOneProxyAcrossDifferentSshuttleHostForSameTarget(t *testing.T) {
	spawner := &fakeSpawner{}
	m, clock := newTestManager(spawner)

	done := make(chan error, 1)
	go func() {
		done <- m.EnsureProxy(context.Background(), "10.0.5.1", "6443", "user@bastion-a", 60*time.Second)
	}()
	waitForTimers(t, clock, 1)
	clock.timers[0].Fire()
	if err := <-done; err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}

	// Different sshuttleHost, same target — dedup wins, no second spawn.
	if err := m.EnsureProxy(context.Background(), "10.0.5.1", "6443", "user@bastion-b", 60*time.Second); err != nil {
		t.Fatalf("second EnsureProxy: %v", err)
	}
	if spawner.callCount() != 1 {
		t.Errorf("spawn called %d times, want 1", spawner.callCount())
	}
}

func TestEnsureProxyUnderSystemdChecksIsActiveFirst(t *testing.T) {
	execer := &scriptedExecer{handler: func(file string, args []string) (shell.Output, error) {
		if file == "systemctl" && args[1] == "is-active" {
			return shell.Output{Stdout: "inactive\n"}, &shell.ExitError{Stdout: "inactive\n", ExitCode: 3, Err: errAnyNonZero}
		}
		return shell.Output{}, nil
	}}
	m := New(&fakeClock{}, execer, (&fakeSpawner{}).spawn, underSystemd)

	if err := m.EnsureProxy(context.Background(), "10.1.2.3", "6443", "user@bastion", 60*time.Second); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}

	call := execer.calls[0]
	if call.file != "systemctl" {
		t.Fatalf("first call file = %q, want systemctl", call.file)
	}
	wantArgs := []string{"--user", "is-active", "capi-shell-api-endpoint-proxy-sshuttle-10.1.2.3:6443.service"}
	if strings.Join(call.args, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("args = %v, want %v", call.args, wantArgs)
	}
}

func TestEnsureProxyUnderSystemdSpawnsViaSystemdRunWhenNoUnitIsActive(t *testing.T) {
	execer := &scriptedExecer{handler: func(file string, args []string) (shell.Output, error) {
		if file == "systemctl" && args[1] == "is-active" {
			return shell.Output{Stdout: "inactive\n"}, &shell.ExitError{Stdout: "inactive\n", ExitCode: 3, Err: errAnyNonZero}
		}
		return shell.Output{}, nil
	}}
	m := New(&fakeClock{}, execer, (&fakeSpawner{}).spawn, underSystemd)

	if err := m.EnsureProxy(context.Background(), "10.1.2.4", "6443", "user@bastion", 60*time.Second); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}

	var runCall *recordedCall
	for i := range execer.calls {
		if execer.calls[i].file == "systemd-run" {
			runCall = &execer.calls[i]
		}
	}
	if runCall == nil {
		t.Fatalf("no systemd-run call among %v", execer.calls)
	}
	wantArgs := []string{
		"--user",
		"--unit=capi-shell-api-endpoint-proxy-sshuttle-10.1.2.4:6443.service",
		"--property=RuntimeMaxSec=60",
		"--property=Restart=on-failure",
		"--property=RestartSec=2",
		"--property=StartLimitIntervalSec=60",
		"--property=StartLimitBurst=3",
		"--collect",
		"--slice=capi-shell-api-endpoint-proxy.slice",
		"--",
		"sshuttle", "-r", "user@bastion", "10.1.2.4:6443",
	}
	if strings.Join(runCall.args, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("args = %v, want %v", runCall.args, wantArgs)
	}
}

func TestEnsureProxyUnderSystemdExtendsRuntimeMaxSecWhenUnitAlreadyActive(t *testing.T) {
	execer := &scriptedExecer{handler: func(file string, args []string) (shell.Output, error) {
		if file == "systemctl" && args[1] == "is-active" {
			return shell.Output{Stdout: "active\n"}, nil
		}
		return shell.Output{}, nil
	}}
	m := New(&fakeClock{}, execer, (&fakeSpawner{}).spawn, underSystemd)

	if err := m.EnsureProxy(context.Background(), "10.1.2.5", "6443", "user@bastion", 60*time.Second); err != nil {
		t.Fatalf("EnsureProxy: %v", err)
	}

	for _, call := range execer.calls {
		if call.file == "systemd-run" {
			t.Fatalf("unexpected systemd-run call when unit already active: %v", call.args)
		}
	}
	var setPropertyCall *recordedCall
	for i := range execer.calls {
		if call := execer.calls[i]; call.file == "systemctl" && call.args[1] == "set-property" {
			setPropertyCall = &execer.calls[i]
		}
	}
	if setPropertyCall == nil {
		t.Fatalf("no systemctl set-property call among %v", execer.calls)
	}
	wantArgs := []string{"--user", "set-property", "capi-shell-api-endpoint-proxy-sshuttle-10.1.2.5:6443.service", "RuntimeMaxSec=60"}
	if strings.Join(setPropertyCall.args, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("args = %v, want %v", setPropertyCall.args, wantArgs)
	}
}

// waitForTimers polls until at least n timers have been registered on the
// fake clock, avoiding a race between EnsureProxy's goroutine and the test.
func waitForTimers(t *testing.T, clock *fakeClock, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clock.mu.Lock()
		count := len(clock.timers)
		clock.mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d timers to be registered", n)
}

type recordedCall struct {
	file string
	args []string
}

// scriptedExecer lets a test script per-call responses (e.g. "systemctl
// is-active" fails while everything else succeeds), recording every call
// made for later assertions.
type scriptedExecer struct {
	mu      sync.Mutex
	calls   []recordedCall
	handler func(file string, args []string) (shell.Output, error)
}

func (e *scriptedExecer) ExecFile(_ context.Context, file string, args []string, _ shell.Options) (shell.Output, error) {
	e.mu.Lock()
	e.calls = append(e.calls, recordedCall{file: file, args: args})
	e.mu.Unlock()
	return e.handler(file, args)
}

var errAnyNonZero = &nonZeroExit{}

type nonZeroExit struct{}

func (*nonZeroExit) Error() string { return "exit status 3" }
