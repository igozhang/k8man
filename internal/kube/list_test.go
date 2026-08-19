package kube

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestListViaWatchCacheFallback(t *testing.T) {
	var calls []string
	got, err := listViaWatchCache(10, func(o metav1.ListOptions) ([]string, error) {
		calls = append(calls, o.ResourceVersion)
		if o.ResourceVersion == "0" {
			return nil, fmt.Errorf("resourceVersion: invalid")
		}
		return []string{"ok"}, nil
	})
	if err != nil || len(got) != 1 || got[0] != "ok" {
		t.Fatalf("got %v %v", got, err)
	}
	if len(calls) != 2 || calls[0] != "0" || calls[1] != "" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestListViaWatchCacheHit(t *testing.T) {
	calls := 0
	got, err := listViaWatchCache(5, func(o metav1.ListOptions) ([]string, error) {
		calls++
		if o.ResourceVersion != "0" {
			t.Fatalf("want rv=0, got %q", o.ResourceVersion)
		}
		return []string{"cached"}, nil
	})
	if err != nil || calls != 1 || got[0] != "cached" {
		t.Fatalf("got %v calls=%d err=%v", got, calls, err)
	}
}
