package httpserver

import (
	"fmt"
	"net/http"
)

// hostCheckMiddleware rejects any request whose Host header isn't exactly
// "127.0.0.1" or "127.0.0.1:<port>" — deliberately narrower than
// "localhost", so MCP client configs must target the literal loopback IP.
// This sits in front of the go-sdk's own (broader) DNS-rebinding
// protection, which stays enabled as a backstop.
func hostCheckMiddleware(port int, next http.Handler) http.Handler {
	withPort := fmt.Sprintf("127.0.0.1:%d", port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1" && r.Host != withPort {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusMisdirectedRequest)
			_, _ = w.Write([]byte("Misdirected Request"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
