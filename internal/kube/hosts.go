package kube

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
)

// HostsResolver maps hostnames to IPs for private resolution (app-level hosts).
type HostsResolver struct {
	mu sync.RWMutex
	m  map[string]string // lowercase hostname -> ip
}

func NewHostsResolver() *HostsResolver {
	return &HostsResolver{m: map[string]string{}}
}

func (h *HostsResolver) Replace(entries map[string]string) {
	next := make(map[string]string, len(entries))
	for host, ip := range entries {
		host = strings.ToLower(strings.TrimSpace(host))
		ip = strings.TrimSpace(ip)
		if host == "" || ip == "" {
			continue
		}
		next[host] = ip
	}
	h.mu.Lock()
	h.m = next
	h.mu.Unlock()
}

func (h *HostsResolver) Lookup(host string) string {
	if h == nil {
		return ""
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.m[host]
}

// ApplyDial rewrites TCP dials so configured hostnames connect to mapped IPs.
// TLS ServerName stays the original hostname from rest.Config.Host.
func (h *HostsResolver) ApplyDial(cfg *rest.Config) {
	if cfg == nil || h == nil {
		return
	}
	base := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	cfg.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return base.DialContext(ctx, network, address)
		}
		if ip := h.Lookup(host); ip != "" {
			address = net.JoinHostPort(ip, port)
		}
		return base.DialContext(ctx, network, address)
	}
}
