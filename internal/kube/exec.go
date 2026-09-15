package kube

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	spdystream "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clienttransport "k8s.io/client-go/transport"
)

// ExecOptions drives an interactive pod exec session (TTY).
type ExecOptions struct {
	Namespace string
	Pod       string
	Container string
	Command   []string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Resize    remotecommand.TerminalSizeQueue
}

// ExecTTY streams an interactive shell into the pod.
// Default command is ["/bin/sh"]; caller may pass Command explicitly.
func (c *Clients) ExecTTY(ctx context.Context, opt ExecOptions) error {
	if c == nil || c.Clientset == nil || c.Config == nil {
		return fmt.Errorf("kube clients not ready")
	}
	ns := strings.TrimSpace(opt.Namespace)
	pod := strings.TrimSpace(opt.Pod)
	if ns == "" || pod == "" {
		return fmt.Errorf("namespace and pod required")
	}
	cmd := opt.Command
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}
	container := strings.TrimSpace(opt.Container)

	// Streaming exec must not inherit rest.Config.Timeout (prepareRESTConfig sets 30s),
	// or the apiserver URL gets ?timeout=30s and the session is killed quickly.
	req := c.Clientset.CoreV1().RESTClient().Post().
		Timeout(0).
		Resource("pods").
		Name(pod).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdin:     opt.Stdin != nil,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	cfg := rest.CopyConfig(c.Config)
	cfg.Timeout = 0
	executor, err := newPodExecExecutor(cfg, req.URL())
	if err != nil {
		return err
	}

	stream := remotecommand.StreamOptions{
		Stdin:             opt.Stdin,
		Stdout:            opt.Stdout,
		Tty:               true,
		TerminalSizeQueue: opt.Resize,
	}
	// With TTY, apiserver merges stderr into stdout; do not attach a separate stderr.
	if opt.Stderr != nil && opt.Stdout == nil {
		stream.Stderr = opt.Stderr
	}

	return executor.StreamWithContext(ctx, stream)
}

func newPodExecExecutor(cfg *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	spdyExec, err := newSPDYExecutorDialAware(cfg, http.MethodPost, u)
	if err != nil {
		return nil, err
	}
	// client-go WebSocket RoundTripper 不读 rest.Config.Dial（Hosts 映射失效）。
	// 有自定义 Dial 时只用 SPDY，避免先 WS DNS 失败且无法回退。
	if cfg != nil && cfg.Dial != nil {
		return spdyExec, nil
	}
	wsExec, err := remotecommand.NewWebSocketExecutor(cfg, http.MethodGet, u.String())
	if err != nil {
		return spdyExec, nil
	}
	return remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
}

// newSPDYExecutorDialAware builds SPDY exec that honors rest.Config.Dial (app Hosts).
// Stock remotecommand.NewSPDYExecutor ignores Dial because SpdyRoundTripper dials on its own.
func newSPDYExecutorDialAware(cfg *rest.Config, method string, u *url.URL) (remotecommand.Executor, error) {
	base, err := dialAwareUpgradeTransport(cfg)
	if err != nil {
		return nil, err
	}
	upgradeRT, err := spdystream.NewRoundTripperWithConfig(spdystream.RoundTripperConfig{
		UpgradeTransport: base,
		PingPeriod:       5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	wrapper, err := rest.HTTPWrappersForConfig(cfg, upgradeRT)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewSPDYExecutorForTransports(wrapper, upgradeRT, method, u)
}

func dialAwareUpgradeTransport(cfg *rest.Config) (http.RoundTripper, error) {
	if cfg == nil {
		return nil, fmt.Errorf("rest config required")
	}
	transportCfg, err := cfg.TransportConfig()
	if err != nil {
		return nil, err
	}
	tlsConfig, err := clienttransport.TLSConfigFor(transportCfg)
	if err != nil {
		return nil, err
	}
	dial := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if transportCfg.DialHolder != nil && transportCfg.DialHolder.Dial != nil {
		dial = transportCfg.DialHolder.Dial
	}
	proxy := cfg.Proxy
	if proxy == nil {
		proxy = http.ProxyFromEnvironment
	}
	// Empty TLSNextProto disables HTTP/2 so SPDY upgrade can negotiate on HTTP/1.1.
	return &http.Transport{
		Proxy:               proxy,
		DialContext:         dial,
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSNextProto:        map[string]func(authority string, c *tls.Conn) http.RoundTripper{},
	}, nil
}

// PodContainerNames returns regular container names (not init).
func PodContainerNames(pod *corev1.Pod) []string {
	if pod == nil {
		return nil
	}
	out := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		if n := strings.TrimSpace(c.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}
