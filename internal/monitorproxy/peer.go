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
)

// procNetTCP is where the kernel lists TCP sockets with their owners; a
// variable so tests can point it at a fixture.
var procNetTCP = "/proc/net/tcp"

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
// connection, from the client's socket entry in /proc/net/tcp: the one whose
// local address is this connection's remote address. Sockets cannot be told
// apart by address alone (any local user can see the listeners and connect
// to them), so who is calling has to be asked of the kernel.
func peerUID(c net.Conn) (int, error) {
	remote, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok || remote.IP.To4() == nil {
		return 0, errors.New("peer is not an IPv4 TCP address")
	}
	ip := remote.IP.To4()
	// The kernel prints the address as the host-order word of the IP, which
	// on little-endian machines reads backwards.
	want := fmt.Sprintf("%02X%02X%02X%02X:%04X", ip[3], ip[2], ip[1], ip[0], remote.Port)

	f, err := os.Open(procNetTCP)
	if err != nil {
		return 0, fmt.Errorf("reading socket table: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || !strings.EqualFold(fields[1], want) {
			continue
		}
		var uid int
		if _, err := fmt.Sscanf(fields[7], "%d", &uid); err != nil {
			return 0, fmt.Errorf("parsing socket owner %q: %w", fields[7], err)
		}
		return uid, nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("the peer's socket is not in the socket table")
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
