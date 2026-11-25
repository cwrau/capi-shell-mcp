package monitorproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeAPIServer records what reaches the cluster's API server.
type fakeAPIServer struct {
	*httptest.Server
	requests []string
	auth     []string
	body     []string
}

func newFakeAPIServer(t *testing.T) *fakeAPIServer {
	t.Helper()
	f := &fakeAPIServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.body = append(f.body, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func startProxy(t *testing.T, api *fakeAPIServer) *Proxy {
	t.Helper()
	base, _ := url.Parse(api.URL)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p, err := Start(ctx, func(_ context.Context, target string) (*url.URL, http.RoundTripper, error) {
		if target == "unknown" {
			return nil, nil, context.DeadlineExceeded
		}
		return base, http.DefaultTransport, nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return p
}

func do(t *testing.T, method, u, body string, header map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestForwardsAQueryThroughTheServiceProxyOfTheClusterAPIServer(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)

	code, body := do(t, http.MethodPost, p.URL("kc/ctx/ns/c1", Prometheus)+"/api/v1/query?x=1", "query=up", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "Authorization": "Bearer caller-token",
	})

	if code != http.StatusOK || !strings.Contains(body, "success") {
		t.Fatalf("got %d %q, want the upstream's 200 answer", code, body)
	}
	want := "POST /api/v1/namespaces/monitoring/services/kube-prometheus-stack-prometheus:http-web/proxy/api/v1/query?x=1"
	if len(api.requests) != 1 || api.requests[0] != want {
		t.Errorf("upstream saw %v, want [%s]", api.requests, want)
	}
	if api.body[0] != "query=up" {
		t.Errorf("upstream body = %q, want the query form", api.body[0])
	}
	if api.auth[0] != "" {
		t.Errorf("the caller's Authorization header reached the cluster: %q", api.auth[0])
	}
}

func TestOtherBackendsMapToTheirServices(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)

	do(t, http.MethodGet, p.URL("t", Loki)+"/loki/api/v1/labels", "", nil)
	do(t, http.MethodGet, p.URL("t", Alertmanager)+"/api/v2/alerts", "", nil)

	want := []string{
		"GET /api/v1/namespaces/monitoring/services/loki:http-metrics/proxy/loki/api/v1/labels",
		"GET /api/v1/namespaces/monitoring/services/kube-prometheus-stack-alertmanager:http-web/proxy/api/v2/alerts",
	}
	if strings.Join(api.requests, "|") != strings.Join(want, "|") {
		t.Errorf("upstream saw %v, want %v", api.requests, want)
	}
}

func TestRefusesAnythingButTheReadEndpoints(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)

	for name, tc := range map[string]struct{ method, backend, path string }{
		"prometheus admin API":          {http.MethodPost, Prometheus, "/api/v1/admin/tsdb/delete_series"},
		"prometheus lifecycle reload":   {http.MethodPost, Prometheus, "/-/reload"},
		"prometheus delete method":      {http.MethodDelete, Prometheus, "/api/v1/query"},
		"alertmanager creating silence": {http.MethodPost, Alertmanager, "/api/v2/silences"},
		"alertmanager posting alerts":   {http.MethodPost, Alertmanager, "/api/v2/alerts"},
		"loki push":                     {http.MethodPost, Loki, "/loki/api/v1/push"},
		"loki delete":                   {http.MethodPost, Loki, "/loki/api/v1/delete"},
		"path traversal":                {http.MethodGet, Prometheus, "/api/v1/query/../admin/tsdb/snapshot"},
		"unlisted GET":                  {http.MethodGet, Prometheus, "/api/v1/status/config"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := do(t, tc.method, p.URL("t", tc.backend)+tc.path, "", nil)
			if code != http.StatusForbidden && code != http.StatusNotFound {
				t.Errorf("got %d, want the request refused", code)
			}
		})
	}
	if len(api.requests) != 0 {
		t.Errorf("refused requests reached the cluster: %v", api.requests)
	}
}

func TestRequiresTheSecretAndAKnownBackend(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)

	good := p.URL("t", Prometheus) + "/api/v1/labels"
	for name, u := range map[string]string{
		"wrong secret":    strings.Replace(good, p.secret, strings.Repeat("0", len(p.secret)), 1),
		"no secret":       "http://" + p.ln.Addr().String() + "/api/v1/labels",
		"unknown backend": p.URL("t", "grafana") + "/api/v1/labels",
	} {
		t.Run(name, func(t *testing.T) {
			if code, _ := do(t, http.MethodGet, u, "", nil); code != http.StatusNotFound {
				t.Errorf("got %d, want 404", code)
			}
		})
	}
	if code, _ := do(t, http.MethodGet, good, "", nil); code != http.StatusOK {
		t.Errorf("good URL got %d, want 200", code)
	}
}

func TestAnUnreachableTargetIsABadGateway(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)

	code, _ := do(t, http.MethodGet, p.URL("unknown", Prometheus)+"/api/v1/labels", "", nil)
	if code != http.StatusBadGateway {
		t.Errorf("got %d, want 502", code)
	}
}

// Alertmanager's client keeps only the host of its URL, so each target gets
// an address of its own, and the path is whatever the client asks for.
func TestAlertmanagerIsReachedByAddressNotByPath(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)

	u1, u2 := p.URL("kc/ctx/ns/c1", Alertmanager), p.URL("kc/ctx/ns/c2", Alertmanager)
	if u1 == u2 || u1 != p.URL("kc/ctx/ns/c1", Alertmanager) {
		t.Fatalf("addresses %q, %q: want one stable address per target", u1, u2)
	}
	if parsed, _ := url.Parse(u1); parsed.Path != "" {
		t.Fatalf("%q has a path; the client would drop it", u1)
	}

	if code, _ := do(t, http.MethodGet, u1+"/api/v2/alerts", "", nil); code != http.StatusOK {
		t.Errorf("GET alerts got %d, want 200", code)
	}
	for _, refused := range []struct{ method, path string }{
		{http.MethodPost, "/api/v2/silences"},
		{http.MethodPost, "/api/v2/alerts"},
		{http.MethodDelete, "/api/v2/silence/abc"},
		{http.MethodGet, "/-/reload"},
	} {
		if code, _ := do(t, refused.method, u1+refused.path, "{}", nil); code != http.StatusForbidden {
			t.Errorf("%s %s got %d, want 403", refused.method, refused.path, code)
		}
	}
	want := "GET /api/v1/namespaces/monitoring/services/kube-prometheus-stack-alertmanager:http-web/proxy/api/v2/alerts"
	if len(api.requests) != 1 || api.requests[0] != want {
		t.Errorf("upstream saw %v, want only [%s]", api.requests, want)
	}
}

// The cardinality guardrail of the upstream query tools reads TSDB statistics.
func TestForwardsTheTSDBStatsReadByTheCardinalityGuardrail(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)

	if code, _ := do(t, http.MethodGet, p.URL("t", Prometheus)+"/api/v1/status/tsdb?limit=10", "", nil); code != http.StatusOK {
		t.Errorf("got %d, want 200", code)
	}
}

// A connection from another user, or one whose owner cannot be found, is
// refused even though the address is right. The test fixture stands in for
// /proc/net/tcp: the kernel's view of who owns the client socket.
func TestRefusesConnectionsNotOwnedByThisUser(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)
	good := p.URL("t", Prometheus) + "/api/v1/labels"
	alert := p.URL("t", Alertmanager) + "/api/v2/alerts"

	orig := procNetTCP
	t.Cleanup(func() { procNetTCP = orig })

	// A header and no matching row: the owner cannot be found.
	empty := t.TempDir() + "/tcp"
	if err := os.WriteFile(empty, []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	procNetTCP = empty
	for _, u := range []string{good, alert} {
		if code, _ := do(t, http.MethodGet, u, "", nil); code != http.StatusNotFound {
			t.Errorf("%s: unknown owner got %d, want 404", u, code)
		}
	}

	http.DefaultClient.CloseIdleConnections() // the owner is looked up once per connection
	// The kernel's own table has the test's client sockets, owned by us.
	procNetTCP = orig
	for _, u := range []string{good, alert} {
		if code, _ := do(t, http.MethodGet, u, "", nil); code != http.StatusOK {
			t.Errorf("%s: own connection got %d, want 200", u, code)
		}
	}
}

func TestPeerUIDReadsTheOwnerFromTheSocketTable(t *testing.T) {
	table := t.TempDir() + "/tcp"
	// 127.0.0.1:40000 owned by uid 4242 (0100007F is 127.0.0.1 backwards, 9C40 is 40000).
	row := "   0: 0100007F:9C40 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  4242        0 12345 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(table, []byte("  sl  local_address rem_address\n"+row), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := procNetTCP
	procNetTCP = table
	t.Cleanup(func() { procNetTCP = orig })

	uid, err := peerUID(fakeConn{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000}})
	if err != nil || uid != 4242 {
		t.Errorf("peerUID = %d, %v; want 4242", uid, err)
	}
	if _, err := peerUID(fakeConn{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40001}}); err == nil {
		t.Error("an unlisted socket was given an owner")
	}
}

type fakeConn struct{ remote net.Addr }

func (c fakeConn) RemoteAddr() net.Addr           { return c.remote }
func (fakeConn) LocalAddr() net.Addr              { return nil }
func (fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }

func TestTempoReadEndpointsOnly(t *testing.T) {
	api := newFakeAPIServer(t)
	p := startProxy(t, api)
	base := p.URL("t", Tempo)

	for _, ok := range []string{"/api/search?q=%7B%7D", "/api/v2/traces/abc123", "/api/v2/search/tags", "/api/v2/search/tag/span.http.url/values"} {
		if code, _ := do(t, http.MethodGet, base+ok, "", nil); code != http.StatusOK {
			t.Errorf("GET %s got %d, want 200", ok, code)
		}
	}
	for _, refused := range []struct{ method, path string }{
		{http.MethodPost, "/api/search"},
		{http.MethodGet, "/flush"},
		{http.MethodGet, "/shutdown"},
		{http.MethodPost, "/ingester/shutdown"},
	} {
		if code, _ := do(t, refused.method, base+refused.path, "", nil); code != http.StatusForbidden {
			t.Errorf("%s %s got %d, want 403", refused.method, refused.path, code)
		}
	}
	want := "GET /api/v1/namespaces/monitoring/services/grafana-tempo:3200/proxy/api/search?q=%7B%7D"
	if len(api.requests) == 0 || api.requests[0] != want {
		t.Errorf("first upstream request = %v, want %s", api.requests, want)
	}
}
