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
const (
	FrontIngressNginx = "ingress-nginx"
	FrontNginx        = "nginx" // standalone / distro nginx, not the Ingress controller
	FrontRancher      = "rancher"
	FrontAPIServer    = "apiserver"
)

const frontProbePath = "/k8man-front-probe"
const frontProbeTimeout = 2 * time.Second

// DetectFrontProxy does one cheap GET against the kubeconfig host (not the API path)
// to tell ingress-nginx default backend from stock nginx HTML. Best-effort; empty if unsure.
func DetectFrontProxy(ctx context.Context, kubeconfigYAML string, hosts *HostsResolver) string {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Host) == "" {
		return ""
	}
	cfg.Timeout = frontProbeTimeout
	cfg.WrapTransport = nil
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
			// Classify the front's own response (nginx 302/404), not the upstream Rancher UI.
			return http.ErrUseLastResponse
		},
	}

	pingCh := make(chan string, 1)
	probeCh := make(chan string, 1)
	go func() { pingCh <- fetchFrontKind(ctx, client, origin+"/ping") }()
	go func() { probeCh <- fetchFrontKind(ctx, client, origin+frontProbePath) }()

	// Wait for both samples. Aborting on ctx.Done after /ping=pong would
	// label 外置 nginx as Rancher when the unknown-path 404 is only a bit slower.
	return pickFrontKind(<-pingCh, <-probeCh)
}

// pickFrontKind prefers the unknown-path fingerprint (ingress-nginx vs 外置 nginx).
// /ping=pong only means Rancher is reachable behind that front — typical 外置 nginx
// reverse-proxies /ping and /k8s/clusters/... while other paths still hit nginx 404.
func pickFrontKind(pingKind, probeKind string) string {
	if probeKind == FrontIngressNginx || probeKind == FrontNginx {
		return probeKind
	}
	if pingKind == FrontRancher {
		return FrontRancher
	}
	if probeKind != "" {
		return probeKind
	}
	return pingKind
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

// ClassifyFrontProxy maps a tiny HTTP sample to ingress-nginx vs stock nginx vs Rancher vs apiserver.
func ClassifyFrontProxy(status int, server, contentType, body string) string {
	srv := strings.ToLower(strings.TrimSpace(server))
	ct := strings.ToLower(contentType)
	b := strings.TrimSpace(body)
	bl := strings.ToLower(b)

	if strings.EqualFold(b, "pong") {
		return FrontRancher
	}
	if strings.Contains(bl, "default backend - 404") || strings.Contains(bl, "default backend") && status == http.StatusNotFound {
		return FrontIngressNginx
	}
	if strings.Contains(bl, "welcome to nginx") || strings.Contains(bl, "<center>nginx</center>") {
		return FrontNginx
	}
	if status >= 300 && status < 400 {
		if strings.Contains(srv, "nginx") || strings.Contains(srv, "openresty") {
			return FrontNginx
		}
	}
	if strings.Contains(ct, "json") && (strings.Contains(bl, `"kind"`) || strings.Contains(bl, "kubernetes")) {
		return FrontAPIServer
	}
	if strings.Contains(bl, "rancher") && (strings.Contains(ct, "html") || strings.Contains(bl, "ember")) {
		return FrontRancher
	}
	if strings.Contains(srv, "openresty") && status == http.StatusNotFound && strings.Contains(ct, "html") {
		return FrontNginx
	}
	// ingress-nginx default backend is already matched above by body text.
	// Stock nginx `return 404;` is often text/plain + Server: nginx — that is 外置 nginx.
	if (strings.Contains(srv, "nginx") || strings.Contains(srv, "openresty")) && status == http.StatusNotFound {
		return FrontNginx
	}
	return ""
}

func classifyFrontFromErr(err error) string {
	var ge *gatewayHTTPError
	if !errors.As(err, &ge) || ge == nil {
		return ""
	}
	return ClassifyFrontProxy(ge.StatusCode, ge.Server, ge.ContentType, ge.Body)
}
