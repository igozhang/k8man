package kube

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
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

	// Match kubectl: advertise stdin/stdout/stderr on the exec subresource even
	// when TTY merges stderr into stdout on the stream side.
	req := c.Clientset.CoreV1().RESTClient().Post().
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

	executor, err := newPodExecExecutor(c, req.URL())
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

func newPodExecExecutor(c *Clients, u *url.URL) (remotecommand.Executor, error) {
	spdyExec, err := remotecommand.NewSPDYExecutor(c.Config, http.MethodPost, u)
	if err != nil {
		return nil, err
	}
	wsExec, err := remotecommand.NewWebSocketExecutor(c.Config, http.MethodGet, u.String())
	if err != nil {
		return spdyExec, nil
	}
	return remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
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
