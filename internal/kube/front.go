package kube

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Front proxy kinds persisted on the cluster and shown in the UI.
// Only Rancher vs 非 Rancher：外置 nginx / F5 / ingress 等转发层一律归为非 Rancher。
const (
	FrontRancher    = "rancher"
	FrontNonRancher = "non-rancher"

	// Legacy values still present in older DB rows / tests; Classify* no longer emit them.
	FrontIngressNginx = "ingress-nginx"
	FrontNginx        = "nginx"
	FrontAPIServer    = "apiserver"
)

const frontProbePath = "/k8man-front-probe"
const frontProbeTimeout = 2 * time.Second

// DetectFrontProxy does one cheap GET against the kubeconfig host (not the API path)
// to tell Rancher from a front gateway (nginx / F5 / ingress / …). Best-effort; empty if unsure.
func DetectFrontProxy(ctx context.Context, kubeconfigYAML string, hosts *HostsResolver) string {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Host) == "" {
		return ""
	}
	cfg.Timeout = frontProbeTimeout
	cfg.WrapTransport = nil
	cfg.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	if hosts != nil {
		hosts.ApplyDial(cfg)
	}
	rt, err := rest.TransportFor(cfg)
	if err != nil || rt == nil {
		return ""
	}
	origin, err := originOf(cfg.Host)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, frontProbeTimeout)
	defer cancel()

	client := &http.Client{
		Transport: rt,
		Timeout:   frontProbeTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Classify the front's own response (302/404), not the upstream Rancher UI.
			return http.ErrUseLastResponse
		},
	}

	pingCh := make(chan string, 1)
	probeCh := make(chan string, 1)
	go func() { pingCh <- fetchFrontKind(ctx, client, origin+"/ping") }()
	go func() { probeCh <- fetchFrontKind(ctx, client, origin+frontProbePath) }()

	// Wait for both samples. Aborting on ctx.Done after /ping=pong would
	// label a gateway as Rancher when the unknown-path 404 is only a bit slower.
	return pickFrontKind(<-pingCh, <-probeCh)
}

// pickFrontKind prefers the unknown-path fingerprint (非 Rancher 网关).
// /ping=pong only means Rancher is reachable behind that front — typical 外置 nginx
// reverse-proxies /ping and /k8s/clusters/... while other paths still hit the gateway.
func pickFrontKind(pingKind, probeKind string) string {
	if isNonRancherFront(probeKind) {
		return FrontNonRancher
	}
	if pingKind == FrontRancher {
		return FrontRancher
	}
	if probeKind != "" {
		return normalizeFrontKind(probeKind)
	}
	return normalizeFrontKind(pingKind)
}

func isNonRancherFront(kind string) bool {
	switch kind {
	case FrontNonRancher, FrontNginx, FrontIngressNginx:
		return true
	default:
		return false
	}
}

func normalizeFrontKind(kind string) string {
	if isNonRancherFront(kind) {
		return FrontNonRancher
	}
	if kind == FrontRancher {
		return FrontRancher
	}
	return ""
}

func fetchFrontKind(ctx context.Context, client *http.Client, rawURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "k8man-front-probe")
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return ClassifyFrontProxy(resp.StatusCode, resp.Header.Get("Server"), resp.Header.Get("Content-Type"), string(body))
}

func originOf(host string) (string, error) {
	raw := strings.TrimSpace(host)
	if raw == "" {
		return "", io.EOF
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", io.EOF
	}
	return u.Scheme + "://" + u.Host, nil
}

// ClassifyFrontProxy maps a tiny HTTP sample to rancher | non-rancher | empty.
func ClassifyFrontProxy(status int, server, contentType, body string) string {
	srv := strings.ToLower(strings.TrimSpace(server))
	ct := strings.ToLower(contentType)
	b := strings.TrimSpace(body)
	bl := strings.ToLower(b)

	if strings.EqualFold(b, "pong") {
		return FrontRancher
	}
	if strings.Contains(bl, "rancher") && (strings.Contains(ct, "html") || strings.Contains(bl, "ember")) {
		return FrontRancher
	}
	// Direct apiserver JSON is not a front tag.
	if strings.Contains(ct, "json") && (strings.Contains(bl, `"kind"`) || strings.Contains(bl, "kubernetes")) {
		return ""
	}
	if looksLikeNonRancherFront(status, srv, ct, bl) {
		return FrontNonRancher
	}
	return ""
}

func looksLikeNonRancherFront(status int, srv, ct, bl string) bool {
	if strings.Contains(bl, "default backend - 404") || (strings.Contains(bl, "default backend") && status == http.StatusNotFound) {
		return true
	}
	if strings.Contains(bl, "welcome to nginx") || strings.Contains(bl, "<center>nginx</center>") {
		return true
	}
	if gatewayServer(srv) {
		if status >= 300 && status < 400 {
			return true
		}
		if status == http.StatusNotFound {
			return true
		}
		if strings.Contains(ct, "html") && status >= 400 {
			return true
		}
	}
	return false
}

func gatewayServer(srv string) bool {
	for _, s := range []string{
		"nginx", "openresty", "bigip", "f5", "haproxy", "envoy",
		"cloudflare", "trafficserver", "ats/", "microsoft-iis", "caddy",
	} {
		if strings.Contains(srv, s) {
			return true
		}
	}
	return false
}

func classifyFrontFromErr(err error) string {
	var ge *gatewayHTTPError
	if !errors.As(err, &ge) || ge == nil {
		return ""
	}
	return ClassifyFrontProxy(ge.StatusCode, ge.Server, ge.ContentType, ge.Body)
}
