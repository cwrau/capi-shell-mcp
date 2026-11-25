package proxy

import (
	"context"
	"fmt"
	"net"
	"time"
)

const (
	// DefaultReadyTimeout is how long EnsureProxy waits for the API server to
	// answer through a tunnel: sshuttle has to connect over SSH and install
	// its redirect first, which takes seconds.
	DefaultReadyTimeout = 20 * time.Second

	probeInterval    = 500 * time.Millisecond
	probeDialTimeout = 2 * time.Second
)

// ProbeFunc checks that addr ("ip:port" of an API server) can be reached.
type ProbeFunc func(ctx context.Context, addr string) error

// DialProbe is the production ProbeFunc: a TCP connection to addr. Before
// sshuttle's redirect exists a connection goes straight out and times out;
// once it is in place the connection is accepted at once.
func DialProbe(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: probeDialTimeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}

// SetReadiness makes EnsureProxy wait, for up to timeout, until probe says
// the API server can be reached, before it reports a tunnel as ready. Without
// it nothing is checked: a tunnel counts as ready when its unit or process
// is up, which is earlier than when it works (the unit is "active" while
// sshuttle is still connecting), and the first calls through it fail with
// timeouts.
func (m *Manager) SetReadiness(probe ProbeFunc, timeout time.Duration) {
	m.probe = probe
	m.readyTimeout = timeout
}

// waitUntilReachable probes the API server until it answers, the caller's
// context ends, or the ready timeout passes.
func (m *Manager) waitUntilReachable(ctx context.Context, apiServerIP, apiServerPort string) error {
	if m.probe == nil {
		return nil
	}
	addr := net.JoinHostPort(apiServerIP, apiServerPort)
	ctx, cancel := context.WithTimeout(ctx, m.readyTimeout)
	defer cancel()

	var last error
	for {
		if last = m.probe(ctx, addr); last == nil {
			return nil
		}
		// Stop at the deadline (or when the caller gives up) by looking at the
		// context itself, not only at what sleeping reports.
		if ctx.Err() != nil || m.sleepFor(ctx, probeInterval) != nil {
			return fmt.Errorf("proxy: the tunnel to %s is up, but the API server does not answer through it after %s: %w", addr, m.readyTimeout, last)
		}
	}
}

func (m *Manager) sleepFor(ctx context.Context, d time.Duration) error {
	if m.sleep != nil {
		return m.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
