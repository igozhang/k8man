package kube

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

func TestPodContainerNames(t *testing.T) {
	if PodContainerNames(nil) != nil {
		t.Fatal("nil pod")
	}
	pod := &corev1.Pod{}
	pod.Spec.Containers = []corev1.Container{
		{Name: "app"},
		{Name: " sidecar "},
		{Name: ""},
	}
	pod.Spec.InitContainers = []corev1.Container{{Name: "init"}}
	got := PodContainerNames(pod)
	if len(got) != 2 || got[0] != "app" || got[1] != "sidecar" {
		t.Fatalf("got %#v", got)
	}
}

func TestDialAwareUpgradeTransportUsesConfigDial(t *testing.T) {
	called := false
	cfg := &rest.Config{
		Host: "https://example.invalid",
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			called = true
			return nil, context.Canceled
		},
	}
	rt, err := dialAwareUpgradeTransport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ht, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("want *http.Transport, got %T", rt)
	}
	if ht.DialContext == nil {
		t.Fatal("DialContext nil")
	}
	_, _ = ht.DialContext(context.Background(), "tcp", "example.invalid:443")
	if !called {
		t.Fatal("custom Dial was not used")
	}
}

func TestExecRequestOmitsTimeoutQuery(t *testing.T) {
	// rest.Config.Timeout must not leak into streaming exec URLs as ?timeout=30s.
	cfg := &rest.Config{
		Host:    "https://example.invalid",
		APIPath: "/api",
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &corev1.SchemeGroupVersion,
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
		Timeout: 30 * time.Second,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	}
	c, err := rest.RESTClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u := c.Post().
		Timeout(0).
		Namespace("ns").
		Resource("pods").
		Name("p").
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"/bin/sh"},
			Stdin:   true,
			Stdout:  true,
			Stderr:  true,
			TTY:     true,
		}, scheme.ParameterCodec).URL()
	if strings.Contains(u.RawQuery, "timeout=") {
		t.Fatalf("exec URL must not include timeout, got %s", u.RawQuery)
	}
	if !strings.Contains(u.RawQuery, "command") {
		t.Fatalf("expected command in query, got %s", u.RawQuery)
	}
}
