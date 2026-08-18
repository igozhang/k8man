package kube

const (
	DefaultPodLogTail  int64 = 200
	MaxPodLogTail      int64 = 500
	DefaultPodLogBytes int64 = 256 * 1024
)

func ClampPodLogTail(n int64) int64 {
	if n <= 0 {
		return DefaultPodLogTail
	}
	if n > MaxPodLogTail {
		return MaxPodLogTail
	}
	return n
}
