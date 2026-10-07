package httpserver

import (
	"net"
	"net/http"
)

// hostCheckMiddleware rejects any request whose Host header isn't the
// literal loopback IP "127.0.0.1" — deliberately narrower than "localhost",
// so MCP client configs must target the literal loopback IP. The port is
// ignored: a proxy in front of the daemon (e.g. systemd socket-activation
// forwarding a stable port to this process's actual port) may leave the
// Host header's port unrewritten, so only the host itself is a meaningful
// signal here. This sits in front of the go-sdk's own (broader)
// DNS-rebinding protection, which stays enabled as a backstop.
func hostCheckMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusMisdirectedRequest)
			_, _ = w.Write([]byte("Misdirected Request"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
