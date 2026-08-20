package kube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClassifyFrontProxy(t *testing.T) {
	tests := []struct {
		name string
		code int
		srv  string
		ct   string
		body string
		want string
	}{
		{
			name: "ingress default backend",
			code: 404,
			srv:  "nginx",
			ct:   "text/plain",
			body: "default backend - 404\n",
			want: FrontNonRancher,
		},
		{
			name: "stock nginx html 404",
			code: 404,
			srv:  "nginx/1.18.0",
			ct:   "text/html",
			body: "<html>\r\n<head><title>404 Not Found</title></head>\r\n<body>\r\n<center><h1>404 Not Found</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n",
			want: FrontNonRancher,
		},
		{
			name: "welcome to nginx",
			code: 200,
			srv:  "nginx",
			ct:   "text/html",
			body: "<h1>Welcome to nginx!</h1>",
			want: FrontNonRancher,
		},
		{
			name: "nginx 302 to dashboard",
			code: 302,
			srv:  "nginx/1.18.0",
			ct:   "text/html",
			body: "",
			want: FrontNonRancher,
		},
		{
			name: "f5 404",
			code: 404,
			srv:  "BigIP",
			ct:   "text/html",
			body: "",
			want: FrontNonRancher,
		},
		{
			name: "rancher ping",
			code: 200,
			srv:  "nginx",
			ct:   "text/plain",
			body: "pong",
			want: FrontRancher,
		},
		{
			name: "kube json 404",
			code: 404,
			srv:  "",
			ct:   "application/json",
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"not found","code":404}`,
			want: "",
		},
		{
			name: "plain nginx 404 no html",
			code: 404,
			srv:  "nginx",
			ct:   "text/plain",
			body: "404",
			want: FrontNonRancher,
		},
		{
			name: "nginx return 404 empty body",
			code: 404,
			srv:  "nginx",
			ct:   "text/plain; charset=utf-8",
			body: "",
			want: FrontNonRancher,
		},
		{
			name: "unknown",
			code: 200,
			srv:  "istio-envoy",
			ct:   "text/html",
			body: "<html>app</html>",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyFrontProxy(tt.code, tt.srv, tt.ct, tt.body)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestLooksLikeGatewayPageIngressPlain(t *testing.T) {
	resp := &http.Response{
		StatusCode: 404,
		Header:     http.Header{"Content-Type": []string{"text/plain"}, "Server": []string{"nginx"}},
	}
	if !looksLikeGatewayPage(resp) {
		t.Fatal("ingress-nginx default backend should match")
	}
}

func TestDetectFrontProxyIngressVsNginx(t *testing.T) {
	ingress := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "nginx")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("default backend - 404\n"))
	}))
	t.Cleanup(ingress.Close)

	stock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeNginx404(w)
	}))
	t.Cleanup(stock.Close)

	// 外置 nginx 反代 Rancher：/ping=pong，未知路径仍是 nginx 404 → 非 Rancher
	behindNginx := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = w.Write([]byte("pong"))
			return
		}
		writeNginx404(w)
	}))
	t.Cleanup(behindNginx.Close)

	rancher := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = w.Write([]byte("pong"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>Rancher ember-app</body></html>`))
	}))
	t.Cleanup(rancher.Close)

	ctx := context.Background()
	if got := DetectFrontProxy(ctx, kubeconfigYAML(ingress.URL+"/k8s/clusters/local"), nil); got != FrontNonRancher {
		t.Fatalf("ingress: got %q", got)
	}
	if got := DetectFrontProxy(ctx, kubeconfigYAML(stock.URL+"/k8s/clusters/local"), nil); got != FrontNonRancher {
		t.Fatalf("stock nginx: got %q", got)
	}
	if got := DetectFrontProxy(ctx, kubeconfigYAML(behindNginx.URL+"/k8s/clusters/local"), nil); got != FrontNonRancher {
		t.Fatalf("rancher behind nginx: got %q want non-rancher", got)
	}
	if got := DetectFrontProxy(ctx, kubeconfigYAML(rancher.URL+"/k8s/clusters/local"), nil); got != FrontRancher {
		t.Fatalf("rancher: got %q", got)
	}
}

func TestDetectFrontProxyWaitsForSlowNginxProbe(t *testing.T) {
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = w.Write([]byte("pong"))
			return
		}
		time.Sleep(1200 * time.Millisecond)
		writeNginx404(w)
	}))
	t.Cleanup(front.Close)

	ctx := context.Background()
	if got := DetectFrontProxy(ctx, kubeconfigYAML(front.URL+"/k8s/clusters/local"), nil); got != FrontNonRancher {
		t.Fatalf("slow nginx 404 after pong: got %q want non-rancher", got)
	}
}

func TestDetectFrontProxyNginxRedirect(t *testing.T) {
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = w.Write([]byte("pong"))
			return
		}
		w.Header().Set("Server", "nginx/1.18.0")
		w.Header().Set("Location", "/dashboard/")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(front.Close)

	ctx := context.Background()
	if got := DetectFrontProxy(ctx, kubeconfigYAML(front.URL+"/k8s/clusters/local"), nil); got != FrontNonRancher {
		t.Fatalf("nginx 302: got %q want non-rancher", got)
	}
}

func TestPickFrontKind(t *testing.T) {
	tests := []struct {
		ping, probe, want string
	}{
		{FrontRancher, FrontNonRancher, FrontNonRancher},
		{FrontRancher, FrontNginx, FrontNonRancher},
		{FrontRancher, FrontIngressNginx, FrontNonRancher},
		{FrontRancher, "", FrontRancher},
		{FrontRancher, FrontRancher, FrontRancher},
		{"", FrontNonRancher, FrontNonRancher},
		{"", FrontNginx, FrontNonRancher},
		{"", FrontIngressNginx, FrontNonRancher},
	}
	for _, tt := range tests {
		if got := pickFrontKind(tt.ping, tt.probe); got != tt.want {
			t.Fatalf("ping=%q probe=%q got %q want %q", tt.ping, tt.probe, got, tt.want)
		}
	}
}
