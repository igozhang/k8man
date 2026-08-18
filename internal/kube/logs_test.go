package kube

import "testing"

func TestClampPodLogTail(t *testing.T) {
	cases := []struct {
		in, want int64
	}{
		{0, DefaultPodLogTail},
		{-1, DefaultPodLogTail},
		{50, 50},
		{200, 200},
		{500, 500},
		{501, MaxPodLogTail},
		{99999, MaxPodLogTail},
	}
	for _, tc := range cases {
		if got := ClampPodLogTail(tc.in); got != tc.want {
			t.Fatalf("ClampPodLogTail(%d)=%d want %d", tc.in, got, tc.want)
		}
	}
}
