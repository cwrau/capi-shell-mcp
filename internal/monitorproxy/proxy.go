// Package monitorproxy is a small loopback HTTP proxy that lets the
// metrics and logs tools reach the Prometheus, Alertmanager and Loki that run
// inside a workload cluster, through that cluster's API server (the service
// proxy), with the admin credentials of the cluster.
//
// The API server's service proxy forwards any method to anything the service
// serves, Prometheus's admin API and Alertmanager's silence creation
// included. So the proxy is not a pass-through: each backend has a fixed list
// of read endpoints and methods, and everything else is refused before it
// leaves the daemon.
package monitorproxy

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Backend names, as used in URLs handed out by Proxy.URL.
const (
	Prometheus   = "prometheus"
	Alertmanager = "alertmanager"
	Loki         = "loki"
	Tempo        = "tempo"
)

type route struct {
	methods []string
	path    *regexp.Regexp
}

type backend struct {
	namespace, service, port string
	routes                   []route
}

// The services are the ones kube-prometheus-stack and the loki chart create
// with their default release names. Their endpoints are the query APIs only.
var backends = map[string]backend{
	Prometheus: {
		namespace: "monitoring", service: "kube-prometheus-stack-prometheus", port: "http-web",
		routes: []route{
			{[]string{http.MethodGet, http.MethodPost}, regexp.MustCompile(`^/api/v1/(query|query_range|query_exemplars|series|labels|label/[^/]+/values)$`)},
			{[]string{http.MethodGet}, regexp.MustCompile(`^/api/v1/(metadata|alerts|rules|status/buildinfo|status/tsdb)$`)},
		},
	},
	Alertmanager: {
		namespace: "monitoring", service: "kube-prometheus-stack-alertmanager", port: "http-web",
		// GET only: POST to /api/v2/silences or /api/v2/alerts would write.
		routes: []route{
			{[]string{http.MethodGet}, regexp.MustCompile(`^/api/v2/(alerts|alerts/groups|silences|silence/[^/]+|status|receivers)$`)},
		},
	},
	Loki: {
		namespace: "monitoring", service: "loki", port: "http-metrics",
		routes: []route{
			{[]string{http.MethodGet, http.MethodPost}, regexp.MustCompile(`^/loki/api/v1/(query|query_range|labels|label/[^/]+/values|series|index/stats|index/volume|index/volume_range|patterns|detected_labels|detected_fields)$`)},
			{[]string{http.MethodGet}, regexp.MustCompile(`^/(ready|loki/api/v1/status/buildinfo)$`)},
		},
	},
	// The grafana-tempo service of the grafana-tempo chart; 3200 is Tempo's
	// HTTP API port (the chart names it tempo-prom-metrics).
	Tempo: {
		namespace: "monitoring", service: "grafana-tempo", port: "3200",
		routes: []route{
			{[]string{http.MethodGet}, regexp.MustCompile(`^/api/(search|echo|v2/search/tags|v2/search/tag/.+/values|v2/traces/[^/]+|status/buildinfo)$`)},
		},
	},
}

// Upstream gives the way to a target's API server: the base URL of the
// server and a transport that authenticates to it.
type Upstream func(ctx context.Context, target string) (apiServer *url.URL, rt http.RoundTripper, err error)

const (
	maxRequestBody = 1 << 20
	requestTimeout = 2 * time.Minute
)

// Proxy serves on a random loopback port, under a random secret path prefix
// so that nothing else on the machine can use it by accident.
type Proxy struct {
	secret   string
	upstream Upstream
	ln       net.Listener
	srv      *http.Server

	mu    sync.Mutex
	bound map[string]net.Listener // host-addressed listeners, see addressFor
	done  <-chan struct{}
}

// Start listens on 127.0.0.1 until ctx ends.
func Start(ctx context.Context, upstream Upstream) (*Proxy, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("monitorproxy: generating secret: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("monitorproxy: listening: %w", err)
	}
	p := &Proxy{secret: hex.EncodeToString(raw[:]), upstream: upstream, ln: ln, bound: map[string]net.Listener{}, done: ctx.Done()}
	p.srv = &http.Server{Handler: onlyThisUser(p), ConnContext: peerContext, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.srv.Shutdown(shutdown)
	}()
	return p, nil
}

// URL is the base URL for one backend of one target; the query API paths go
// after it.
//
// Alertmanager's own client keeps only the host of the URL it is given, so a
// path prefix cannot say which cluster it means. Its URL is an address of its
// own instead (see addressFor); the other backends are told apart by path.
func (p *Proxy) URL(target, backendName string) string {
	if backendName == Alertmanager {
		return "http://" + p.addressFor(target, backendName)
	}
	return fmt.Sprintf("http://%s/%s/%s/%s", p.ln.Addr(), p.secret, base64.RawURLEncoding.EncodeToString([]byte(target)), backendName)
}

// addressFor is the host:port of a listener that serves one backend of one
// target, whatever the path. It is a random loopback address and port, which
// is what keeps other local programs from using it. It is made on first use
// and kept until the proxy stops.
func (p *Proxy) addressFor(target, backendName string) string {
	key := backendName + "/" + target
	p.mu.Lock()
	defer p.mu.Unlock()
	if ln, ok := p.bound[key]; ok {
		return ln.Addr().String()
	}
	var ln net.Listener
	for range 8 {
		var r [3]byte
		_, _ = rand.Read(r[:])
		// All of 127.0.0.0/8 is loopback on Linux.
		var err error
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.%d.%d.%d:0", r[0], r[1], r[2])); err == nil {
			break
		}
		ln = nil
	}
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return "127.0.0.1:0" // cannot be reached: the call fails with a connection error
		}
	}
	srv := &http.Server{
		Handler: onlyThisUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.forward(w, r, target, backendName, r.URL.Path)
		})),
		ConnContext:       peerContext,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	p.bound[key] = ln
	go func() {
		<-p.done
		_ = srv.Close()
	}()
	return ln.Addr().String()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	secret, rest, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !ok || subtle.ConstantTimeCompare([]byte(secret), []byte(p.secret)) != 1 {
		http.NotFound(w, r)
		return
	}
	encTarget, rest, ok := strings.Cut(rest, "/")
	backendName, rest, _ := strings.Cut(rest, "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	rawTarget, err := base64.RawURLEncoding.DecodeString(encTarget)
	if _, known := backends[backendName]; err != nil || !known {
		http.NotFound(w, r)
		return
	}
	p.forward(w, r, string(rawTarget), backendName, "/"+rest)
}

// forward sends one request for backendName of target to the cluster, if it
// is one of the backend's read endpoints.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, target, backendName, endpoint string) {
	b := backends[backendName]
	if path.Clean(endpoint) != endpoint {
		http.Error(w, "refused: path is not clean", http.StatusForbidden)
		return
	}
	if !b.allows(r.Method, endpoint) {
		http.Error(w, fmt.Sprintf("refused: %s %s is not one of the read endpoints the proxy forwards to %s", r.Method, endpoint, backendName), http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	apiServer, rt, err := p.upstream(ctx, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	upstreamURL := *apiServer
	upstreamURL.Path = path.Join(apiServer.Path, "/api/v1/namespaces", b.namespace, "services", b.service+":"+b.port, "proxy", endpoint)
	upstreamURL.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL.String(), http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Only what the query APIs need. Authorization is the caller's own (or
	// none) and must not reach the cluster, which is addressed with ours.
	for _, h := range []string{"Content-Type", "Accept", "X-Scope-OrgID"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "proxying to the cluster: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (b backend) allows(method, endpoint string) bool {
	for _, r := range b.routes {
		for _, m := range r.methods {
			if m == method && r.path.MatchString(endpoint) {
				return true
			}
		}
	}
	return false
}
