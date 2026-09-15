package owner

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func boolPtr(v bool) *bool { return &v }

func TestResolvePodController(t *testing.T) {
	rsMap := map[string]string{"app-rs": "app"}

	t.Run("deployment via rs", func(t *testing.T) {
		kind, name := ResolvePodController([]metav1.OwnerReference{{
			Kind: "ReplicaSet", Name: "app-rs", Controller: boolPtr(true),
		}}, rsMap)
		if kind != "Deployment" || name != "app" {
			t.Fatalf("got %s/%s", kind, name)
		}
	})

	t.Run("statefulset", func(t *testing.T) {
		kind, name := ResolvePodController([]metav1.OwnerReference{{
			Kind: "StatefulSet", Name: "db", Controller: boolPtr(true),
		}}, nil)
		if kind != "StatefulSet" || name != "db" {
			t.Fatalf("got %s/%s", kind, name)
		}
	})

	t.Run("daemonset", func(t *testing.T) {
		kind, name := ResolvePodController([]metav1.OwnerReference{{
			Kind: "DaemonSet", Name: "agent", Controller: boolPtr(true),
		}}, nil)
		if kind != "DaemonSet" || name != "agent" {
			t.Fatalf("got %s/%s", kind, name)
		}
	})

	t.Run("orphan rs", func(t *testing.T) {
		kind, name := ResolvePodController([]metav1.OwnerReference{{
			Kind: "ReplicaSet", Name: "orphan-rs", Controller: boolPtr(true),
		}}, rsMap)
		if kind != "ReplicaSet" || name != "orphan-rs" {
			t.Fatalf("got %s/%s", kind, name)
		}
	})

	t.Run("empty", func(t *testing.T) {
		kind, name := ResolvePodController(nil, nil)
		if kind != "" || name != "" {
			t.Fatalf("got %s/%s", kind, name)
		}
	})
}

func TestBuildRSDeployMap(t *testing.T) {
	m := BuildRSDeployMap(map[string][]metav1.OwnerReference{
		"rs1": {{Kind: "Deployment", Name: "dep1", Controller: boolPtr(true)}},
		"rs2": {{Kind: "Job", Name: "job1", Controller: boolPtr(true)}},
	})
	if m["rs1"] != "dep1" {
		t.Fatalf("rs1: %q", m["rs1"])
	}
	if _, ok := m["rs2"]; ok {
		t.Fatalf("rs2 should be absent")
	}
}
