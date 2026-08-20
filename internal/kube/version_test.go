package kube

import (
	"strings"
	"testing"
)

func TestParseKubeMajorMinor(t *testing.T) {
	tests := []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"v1.18.20", 1, 18, true},
		{"v1.28.5", 1, 28, true},
		{"1.30.2+rke2r1", 1, 30, true},
		{"v1.33.0", 1, 33, true},
		{"v1.28+", 1, 28, true},
		{"bogus", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		maj, min, ok := parseKubeMajorMinor(tt.in)
		if ok != tt.ok || maj != tt.major || min != tt.minor {
			t.Fatalf("%q: got %d.%d ok=%v want %d.%d ok=%v", tt.in, maj, min, ok, tt.major, tt.minor, tt.ok)
		}
	}
}

func TestKubeVersionHint(t *testing.T) {
	if h := kubeVersionHint("v1.28.5"); h != "" {
		t.Fatalf("in-range hint: %q", h)
	}
	if h := kubeVersionHint("v1.16.15"); !strings.Contains(h, "1.18") {
		t.Fatalf("low hint: %q", h)
	}
	if h := kubeVersionHint("v1.34.0"); !strings.Contains(h, "1.33") {
		t.Fatalf("high hint: %q", h)
	}
}
