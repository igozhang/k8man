package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
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
