package kube

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// LookupHostIP resolves host with the process default resolver (same as k8man connectivity).
// Returns preferred IPv4 (else first IPv6) and all resolved IPs.
func LookupHostIP(ctx context.Context, host string) (string, []string, error) {
	host = normalizeLookupHost(host)
	if host == "" {
		return "", nil, errors.New("host required")
	}
	if ip := net.ParseIP(host); ip != nil {
		s := ip.String()
		return s, []string{s}, nil
	}
	if err := validateLookupHost(host); err != nil {
		return "", nil, err
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", nil, err
	}
	if len(addrs) == 0 {
		return "", nil, fmt.Errorf("no addresses for %s", host)
	}
	ips := make([]string, 0, len(addrs))
	var preferred string
	for _, a := range addrs {
		if a.IP == nil {
			continue
		}
		s := a.IP.String()
		ips = append(ips, s)
		if preferred == "" {
			if v4 := a.IP.To4(); v4 != nil {
				preferred = v4.String()
			}
		}
	}
	if preferred == "" && len(ips) > 0 {
		preferred = ips[0]
	}
	if preferred == "" {
		return "", nil, fmt.Errorf("no addresses for %s", host)
	}
	return preferred, ips, nil
}

func normalizeLookupHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

func validateLookupHost(host string) error {
	if len(host) == 0 || len(host) > 253 {
		return errors.New("invalid host")
	}
	if strings.ContainsAny(host, "/:\\") {
		return errors.New("invalid host")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return errors.New("invalid host")
		}
	}
	return nil
}
