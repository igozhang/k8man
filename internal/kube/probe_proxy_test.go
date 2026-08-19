package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

const rancherPrefix = "/k8s/clusters/local"

func TestProbeThroughNginxStripPrefix(t *testing.T) {
	backend := httptest.NewServer(fakeKubeAPI())
	t.Cleanup(backend.Close)

	front := httptest.NewTLSServer(nginxFront(stripPrefixProxy(backend.URL, rancherPrefix), rancherPrefix))
	t.Cleanup(front.Close)

	res := probeViaNginx(t, front, rancherPrefix, nil)
	if res.Status != "ok" {
		t.Fatalf("status=%s err=%s", res.Status, res.LastError)
	}
	if res.PermType != "cluster" {
		t.Fatalf("permType=%s", res.PermType)
	}
	if !containsStr(res.Namespaces, "default") {
		t.Fatalf("namespaces=%v", res.Namespaces)
	}
}

func TestProbeThroughNginxPassThrough(t *testing.T) {
	backend := httptest.NewServer(fakeKubeAPI())
	t.Cleanup(backend.Close)

	front := httptest.NewTLSServer(nginxFront(passThroughProxy(backend.URL), rancherPrefix))
	t.Cleanup(front.Close)

	res := probeViaNginx(t, front, rancherPrefix, nil)
	if res.Status != "ok" || res.PermType != "cluster" {
		t.Fatalf("status=%s perm=%s err=%s", res.Status, res.PermType, res.LastError)
	}
}

func TestProbeThroughNginxWithHosts(t *testing.T) {
	backend := httptest.NewServer(fakeKubeAPI())
	t.Cleanup(backend.Close)

	front := httptest.NewTLSServer(nginxFront(stripPrefixProxy(backend.URL, rancherPrefix), rancherPrefix))
	t.Cleanup(front.Close)

	host, port := mustHostPort(t, front.URL)
	name := "paas-ywmon.example.local"
	kc := kubeconfigYAML(fmt.Sprintf("https://%s:%s%s", name, port, rancherPrefix))
	resolver := NewHostsResolver()
	resolver.Replace(map[string]string{name: host})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := Probe(ctx, kc, resolver)
	if res.Status != "ok" {
		t.Fatalf("hosts+nginx probe failed: %s", res.LastError)
	}
}

func TestProbeNginxHTML404IsGatewayError(t *testing.T) {
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeNginx404(w)
	}))
	t.Cleanup(front.Close)

	res := probeViaNginx(t, front, rancherPrefix, nil)
	if res.Status != "error" {
		t.Fatalf("want error, got %+v", res)
	}
	if !strings.Contains(res.LastError, "前端网关") && !strings.Contains(res.LastError, "nginx") {
		t.Fatalf("want gateway hint, got %q", res.LastError)
	}
	if strings.Contains(res.LastError, "get namespaces") && !strings.Contains(res.LastError, "前端网关") && !strings.Contains(res.LastError, "外置 nginx") {
		t.Fatalf("still wrapped as k8s namespaces 404: %q", res.LastError)
	}
	if res.FrontProxy != FrontNginx {
		t.Fatalf("frontProxy=%q want nginx", res.FrontProxy)
	}
}

func TestProbePrefersOriginRancherPing(t *testing.T) {
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = w.Write([]byte("pong"))
			return
		}
		writeNginx404(w)
	}))
	t.Cleanup(front.Close)

	res := probeViaNginx(t, front, rancherPrefix, nil)
	if res.Status != "error" {
		t.Fatalf("want error, got %+v", res)
	}
	if res.FrontProxy != FrontRancher {
		t.Fatalf("frontProxy=%q want rancher from origin /ping", res.FrontProxy)
	}
}

func TestLooksLikeGatewayPage(t *testing.T) {
	html404 := &http.Response{
		StatusCode: 404,
		Header:     http.Header{"Content-Type": []string{"text/html"}, "Server": []string{"nginx"}},
	}
	if !looksLikeGatewayPage(html404) {
		t.Fatal("nginx html 404 should match")
	}
	k8s404 := &http.Response{
		StatusCode: 404,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	if looksLikeGatewayPage(k8s404) {
		t.Fatal("k8s json 404 must pass through")
	}
	rancher401 := &http.Response{
		StatusCode: 401,
		Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}, "Server": []string{"nginx/1.21.0"}},
	}
	if looksLikeGatewayPage(rancher401) {
		t.Fatal("rancher 401 must pass through")
	}
}

func TestFriendlyConnectivityErrorGateway(t *testing.T) {
	msg := FriendlyConnectivityError(&gatewayHTTPError{StatusCode: 404, Server: "nginx", URL: "https://x/k8s/clusters/local/api/v1/namespaces"})
	if !strings.Contains(msg, "nginx") || !strings.Contains(msg, "反代") {
		t.Fatalf("got %q", msg)
	}
}

func probeViaNginx(t *testing.T, front *httptest.Server, prefix string, hosts *HostsResolver) ProbeResult {
	t.Helper()
	kc := kubeconfigYAML(front.URL + prefix)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return Probe(ctx, kc, hosts)
}

func kubeconfigYAML(server string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    insecure-skip-tls-verify: true
  name: local
contexts:
- context:
    cluster: local
    user: u
  name: c
current-context: c
users:
- name: u
  user:
    token: dummy-token
`, server)
}

func fakeKubeAPI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, rancherPrefix)
		if path == "" {
			path = "/"
		}
		w.Header().Set("Content-Type", "application/json")
		switch path {
		case "/api/v1/namespaces":
			writeJSON(w, map[string]any{
				"kind":       "NamespaceList",
				"apiVersion": "v1",
				"metadata":   map[string]any{"resourceVersion": "1"},
				"items": []map[string]any{
					{"kind": "Namespace", "apiVersion": "v1", "metadata": map[string]any{"name": "default"}},
				},
			})
		case "/api/v1/nodes":
			writeJSON(w, map[string]any{
				"kind":       "NodeList",
				"apiVersion": "v1",
				"metadata":   map[string]any{"resourceVersion": "1"},
				"items": []map[string]any{
					{"kind": "Node", "apiVersion": "v1", "metadata": map[string]any{"name": "node-1"}},
				},
			})
		case "/version":
			writeJSON(w, map[string]any{"major": "1", "minor": "26", "gitVersion": "v1.26.7"})
		default:
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{
				"kind":       "Status",
				"apiVersion": "v1",
				"status":     "Failure",
				"message":    "not found: " + path,
				"reason":     "NotFound",
				"code":       404,
			})
		}
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}

func writeNginx404(w http.ResponseWriter) {
	w.Header().Set("Server", "nginx")
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("<html>\r\n<head><title>404 Not Found</title></head>\r\n<body>\r\n<center><h1>404 Not Found</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"))
}

func nginxFront(proxy http.Handler, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
			proxy.ServeHTTP(w, r)
			return
		}
		writeNginx404(w)
	})
}

func stripPrefixProxy(backendURL, prefix string) http.Handler {
	u, err := url.Parse(backendURL)
	if err != nil {
		panic(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.Host = u.Host
		p := req.URL.Path
		if p == prefix {
			p = "/"
		} else if strings.HasPrefix(p, prefix+"/") {
			p = strings.TrimPrefix(p, prefix)
		}
		req.URL.Path = p
	}
	return proxy
}

func passThroughProxy(backendURL string) http.Handler {
	u, err := url.Parse(backendURL)
	if err != nil {
		panic(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.Host = u.Host
	}
	return proxy
}

func mustHostPort(t *testing.T, rawURL string) (string, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func containsStr(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}
