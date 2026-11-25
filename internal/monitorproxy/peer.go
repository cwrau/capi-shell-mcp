package monitorproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// procNetTCP is where the kernel lists TCP sockets with their owners; a
// variable so tests can point it at a fixture.
//
// It is read by the server goroutines of the proxy, so tests that point it
// elsewhere go through setProcNetTCP instead of assigning it.
var procNetTCP = func() *atomic.Pointer[string] {
	var p atomic.Pointer[string]
	path := "/proc/net/tcp"
	p.Store(&path)
	return &p
}()

// setProcNetTCP points the socket table at path and returns what puts the
// previous one back.
func setProcNetTCP(path string) (restore func()) {
	prev := procNetTCP.Swap(&path)
	return func() { procNetTCP.Store(prev) }
}

type peerKey struct{}

// peerResult is what is known of the process on the other end of a
// connection: the user that owns its socket, or why that is not known.
type peerResult struct {
	uid int
	err error
}

// peerContext is an http.Server ConnContext that finds out who connected,
// once per connection.
func peerContext(ctx context.Context, c net.Conn) context.Context {
	uid, err := peerUID(c)
	return context.WithValue(ctx, peerKey{}, peerResult{uid: uid, err: err})
}

// peerUID is the user id that owns the client's end of a loopback IPv4
// connection, from the client's socket entry in /proc/net/tcp: the
// established one that has this connection's remote address as its local
// address and this connection's local address as its remote one.
//
// Sockets cannot be told apart by address alone (any local user can see the
// listeners and connect to them), so who is calling has to be asked of the
// kernel. And the local address alone is not unique: two sockets may share a
// local address and port as long as they go to different places, so only the
// whole pair identifies the connection. More than one match, or none, is an
// error rather than a guess.
func peerUID(c net.Conn) (int, error) {
	client, err := procAddress(c.RemoteAddr())
	if err != nil {
		return 0, fmt.Errorf("peer address: %w", err)
	}
	server, err := procAddress(c.LocalAddr())
	if err != nil {
		return 0, fmt.Errorf("local address: %w", err)
	}

	f, err := os.Open(*procNetTCP.Load())
	if err != nil {
		return 0, fmt.Errorf("reading socket table: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	uid, found := 0, 0
	for sc.Scan() {
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[3] != "01" || !strings.EqualFold(fields[1], client) || !strings.EqualFold(fields[2], server) {
			continue
		}
		if _, err := fmt.Sscanf(fields[7], "%d", &uid); err != nil {
			return 0, fmt.Errorf("parsing socket owner %q: %w", fields[7], err)
		}
		found++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	switch found {
	case 0:
		return 0, errors.New("the peer's socket is not in the socket table")
	case 1:
		return uid, nil
	}
	return 0, fmt.Errorf("%d sockets match the connection, refusing to guess", found)
}

// procAddress writes an IPv4 TCP address the way /proc/net/tcp does: the
// host-order word of the IP, which on little-endian machines reads
// backwards, and the port, both in hex.
func procAddress(a net.Addr) (string, error) {
	tcp, ok := a.(*net.TCPAddr)
	if !ok || tcp == nil || tcp.IP.To4() == nil {
		return "", errors.New("not an IPv4 TCP address")
	}
	ip := tcp.IP.To4()
	return fmt.Sprintf("%02X%02X%02X%02X:%04X", ip[3], ip[2], ip[1], ip[0], tcp.Port), nil
}

// onlyThisUser refuses requests from connections that are not owned by the
// user the daemon runs as, and from any whose owner could not be found.
func onlyThisUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, _ := r.Context().Value(peerKey{}).(peerResult)
		if peer.err != nil || peer.uid != os.Geteuid() {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
