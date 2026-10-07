package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostCheckMiddlewareAcceptsBareIP(t *testing.T) {
	called := false
	mw := hostCheckMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler was not called for Host: 127.0.0.1")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHostCheckMiddlewareAcceptsIPWithPort(t *testing.T) {
	called := false
	mw := hostCheckMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "127.0.0.1:4737"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler was not called for Host: 127.0.0.1:4737")
	}
}

func TestHostCheckMiddlewareAcceptsIPWithAnyPort(t *testing.T) {
	// A proxy in front of the daemon (e.g. systemd socket-activation
	// forwarding a stable external port to this process's actual port) may
	// leave the Host header's port unrewritten — only the host itself is a
	// meaningful signal here, so any port must be accepted.
	called := false
	mw := hostCheckMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "127.0.0.1:9999"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler was not called for Host: 127.0.0.1:9999 — any port must be accepted")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHostCheckMiddlewareRejectsLocalhost(t *testing.T) {
	called := false
	mw := hostCheckMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Host = "localhost:4737"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if called {
		t.Fatal("next handler was called for Host: localhost — must be rejected")
	}
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMisdirectedRequest)
	}
}
