package kube

import (
	"context"
	"net"
	"testing"
)

func TestLookupHostIP_localhost(t *testing.T) {
	ip, ips, err := LookupHostIP(context.Background(), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if ip == "" || len(ips) == 0 {
		t.Fatalf("expected at least one IP, got ip=%q ips=%v", ip, ips)
	}
	if net.ParseIP(ip) == nil {
		t.Fatalf("invalid ip %q", ip)
	}
}

func TestLookupHostIP_invalid(t *testing.T) {
	_, _, err := LookupHostIP(context.Background(), "bad/host")
	if err == nil {
		t.Fatal("expected error")
	}
}
