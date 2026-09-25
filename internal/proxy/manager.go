// Package proxy manages sshuttle VPN tunnels (directly, or as systemd-run
// units under a systemd user session) that let locally-spawned commands
// reach a workload cluster's API server when it isn't otherwise routable.
package proxy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
	"golang.org/x/sync/singleflight"
)

const (
	unitPrefix    = "capi-shell-api-endpoint-proxy-sshuttle-"
	unitSuffix    = ".service"
	startupWindow = 1 * time.Second
)

func targetKey(apiServerIP, apiServerPort string) string {
	return apiServerIP + ":" + apiServerPort
}

func unitName(apiServerIP, apiServerPort string) string {
	return unitPrefix + apiServerIP + ":" + apiServerPort + unitSuffix
}

// processEntry tracks a directly-spawned (non-systemd) sshuttle process —
// the fallback used when not running under systemd. The daemon process
// owns the proxy 1:1 here, so in-memory tracking is safe: there's no
// separate long-lived unit for it to drift from. Under systemd, by
// contrast, the transient unit itself is the only source of truth (see
// ensureSystemdProxy) — the daemon can restart and wipe any in-memory
// state while the unit keeps running independently of it, so a local
// cache there would just go stale.
type processEntry struct {
	process *os.Process
	timer   Timer
}

// ProcessSpawner starts sshuttle directly (non-systemd deployments) and
// returns the running command for the caller to Wait()/signal.
type ProcessSpawner func(sshuttleHost, apiServerIP, apiServerPort string) (*exec.Cmd, error)

// DefaultSpawnProcess is the production ProcessSpawner.
func DefaultSpawnProcess(sshuttleHost, apiServerIP, apiServerPort string) (*exec.Cmd, error) {
	cmd := exec.Command("sshuttle", "-r", sshuttleHost, apiServerIP+":"+apiServerPort)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// Manager ensures sshuttle tunnels exist and refreshes their TTL on reuse.
type Manager struct {
	clock          Clock
	execer         shell.Execer
	spawnProcess   ProcessSpawner
	isUnderSystemd func() bool

	mu             sync.Mutex
	processEntries map[string]*processEntry
	group          singleflight.Group
}

func New(clock Clock, execer shell.Execer, spawnProcess ProcessSpawner, isUnderSystemd func() bool) *Manager {
	return &Manager{
		clock:          clock,
		execer:         execer,
		spawnProcess:   spawnProcess,
		isUnderSystemd: isUnderSystemd,
		processEntries: make(map[string]*processEntry),
	}
}

// IsUnderSystemd reports whether the process is running as (a child of) a
// systemd unit, detected the same way systemd itself recommends: presence
// of INVOCATION_ID in the environment.
func IsUnderSystemd() bool {
	_, ok := os.LookupEnv("INVOCATION_ID")
	return ok
}

// EnsureProxy starts (or, if already running, extends the TTL of) a tunnel
// to apiServerIP:apiServerPort via sshuttleHost. Concurrent calls for the
// same target share one attempt.
func (m *Manager) EnsureProxy(ctx context.Context, apiServerIP, apiServerPort, sshuttleHost string, ttl time.Duration) error {
	key := targetKey(apiServerIP, apiServerPort)
	_, err, _ := m.group.Do(key, func() (any, error) {
		if m.isUnderSystemd() {
			return nil, m.ensureSystemdProxy(ctx, apiServerIP, apiServerPort, sshuttleHost, ttl)
		}
		return nil, m.ensureProcessProxy(key, apiServerIP, apiServerPort, sshuttleHost, ttl)
	})
	return err
}

// ensureSystemdProxy treats the systemd unit itself as the only source of
// truth for whether a proxy is running (never an in-memory flag): a fresh
// systemctl is-active check decides whether to extend the existing unit's
// RuntimeMaxSec or spawn a new one. This survives a daemon restart with no
// reconciliation step needed — there's no in-memory state to go stale in
// the first place.
func (m *Manager) ensureSystemdProxy(ctx context.Context, apiServerIP, apiServerPort, sshuttleHost string, ttl time.Duration) error {
	name := unitName(apiServerIP, apiServerPort)
	ttlSeconds := strconv.Itoa(int(ttl.Seconds()))

	if m.isUnitActive(ctx, name) {
		if _, err := m.execer.ExecFile(ctx, "systemctl", []string{
			"--user", "set-property", name, "RuntimeMaxSec=" + ttlSeconds,
		}, shell.Options{}); err != nil {
			return fmt.Errorf("proxy: extending %s: %w", name, err)
		}
		return nil
	}

	if _, err := m.execer.ExecFile(ctx, "systemd-run", []string{
		"--user",
		"--unit=" + name,
		"--property=RuntimeMaxSec=" + ttlSeconds,
		"--property=Restart=on-failure",
		"--property=RestartSec=2",
		"--property=StartLimitIntervalSec=60",
		"--property=StartLimitBurst=3",
		"--collect",
		"--slice=capi-shell-api-endpoint-proxy.slice",
		"--",
		"sshuttle", "-r", sshuttleHost, apiServerIP + ":" + apiServerPort,
	}, shell.Options{}); err != nil {
		return fmt.Errorf("proxy: starting systemd-run tunnel: %w", err)
	}
	return nil
}

// isUnitActive reports whether name is an active systemd unit. Any error
// (including the non-zero exit systemctl is-active makes for every
// non-active state) means "not active" — that's a normal outcome here, not
// a real failure.
func (m *Manager) isUnitActive(ctx context.Context, name string) bool {
	out, err := m.execer.ExecFile(ctx, "systemctl", []string{"--user", "is-active", name}, shell.Options{})
	if err != nil {
		return false
	}
	return strings.TrimSpace(out.Stdout) == "active"
}

func (m *Manager) ensureProcessProxy(key, apiServerIP, apiServerPort, sshuttleHost string, ttl time.Duration) error {
	m.mu.Lock()
	existing, ok := m.processEntries[key]
	m.mu.Unlock()
	if ok {
		existing.timer.Stop()
		existing.timer = m.clock.AfterFunc(ttl, func() { m.killProcessEntry(key) })
		return nil
	}

	cmd, err := m.spawnProcess(sshuttleHost, apiServerIP, apiServerPort)
	if err != nil {
		return fmt.Errorf("proxy: spawning sshuttle: %w", err)
	}

	exited := make(chan struct{}, 1)
	go func() {
		_ = cmd.Wait()
		exited <- struct{}{}
	}()

	startupElapsed := make(chan struct{}, 1)
	startupTimer := m.clock.AfterFunc(startupWindow, func() { startupElapsed <- struct{}{} })

	select {
	case <-exited:
		startupTimer.Stop()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return fmt.Errorf("sshuttle exited early with code %d", code)
	case <-startupElapsed:
	}

	e := &processEntry{process: cmd.Process}
	e.timer = m.clock.AfterFunc(ttl, func() { m.killProcessEntry(key) })
	m.mu.Lock()
	m.processEntries[key] = e
	m.mu.Unlock()

	go func() {
		<-exited
		m.mu.Lock()
		if cur, ok := m.processEntries[key]; ok && cur == e {
			cur.timer.Stop()
			delete(m.processEntries, key)
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) killProcessEntry(key string) {
	m.mu.Lock()
	e, ok := m.processEntries[key]
	if ok {
		delete(m.processEntries, key)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	e.timer.Stop()
	if e.process != nil {
		_ = e.process.Signal(syscall.SIGTERM)
	}
}

// KillAllProcessProxies tears down every directly-spawned (non-systemd)
// tunnel — intended for an on-exit hook. Systemd-managed units are
// deliberately left alone: they're meant to outlive the daemon process
// (that's the whole point of the systemd-is-source-of-truth design), and
// systemd's own RuntimeMaxSec/StartLimit* properties already bound their
// lifetime without our help.
func (m *Manager) KillAllProcessProxies() {
	m.mu.Lock()
	keys := make([]string, 0, len(m.processEntries))
	for k := range m.processEntries {
		keys = append(keys, k)
	}
	m.mu.Unlock()
	for _, key := range keys {
		m.killProcessEntry(key)
	}
}
