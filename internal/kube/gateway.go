package kube

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"k8s.io/client-go/rest"
)

// gatewayHTTPError is returned when a frontend reverse proxy (typically nginx)
// answered instead of the Kubernetes / Rancher API.
type gatewayHTTPError struct {
	StatusCode  int
	Server      string
	URL         string
	ContentType string
}

func (e *gatewayHTTPError) Error() string {
	if e == nil {
		return ""
	}
	srv := strings.TrimSpace(e.Server)
	if srv == "" {
		srv = "gateway"
	}
	return fmt.Sprintf("%s returned HTTP %d for %s", srv, e.StatusCode, e.URL)
}

func wrapGatewayAwareTransport(cfg *rest.Config) {
	if cfg == nil {
		return
	}
	prev := cfg.WrapTransport
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		if prev != nil {
			rt = prev(rt)
		}
		return &gatewayAwareTransport{base: rt}
	}
}

type gatewayAwareTransport struct {
	base http.RoundTripper
}

func (t *gatewayAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if !looksLikeGatewayPage(resp) {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	_ = resp.Body.Close()
	url := ""
	if req != nil && req.URL != nil {
		url = req.URL.String()
	}
	return nil, &gatewayHTTPError{
		StatusCode:  resp.StatusCode,
		Server:      resp.Header.Get("Server"),
		URL:         url,
		ContentType: resp.Header.Get("Content-Type"),
	}
}

func looksLikeGatewayPage(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return false
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "json") {
		return false
	}
	srv := strings.ToLower(resp.Header.Get("Server"))
	if strings.Contains(ct, "text/html") ||
		(strings.Contains(ct, "text/plain") && strings.Contains(srv, "nginx")) {
		return true
	}
	return strings.Contains(srv, "nginx") && ct == ""
}
