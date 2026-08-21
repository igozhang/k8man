package kube

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDecayBySteps(t *testing.T) {
	if got := decayBySteps("critical", 2, "info"); got != "warning" {
		t.Fatalf("imagepull 60d: %s", got)
	}
	if got := decayBySteps("critical", 1, "info"); got != "error" {
		t.Fatalf("30d: %s", got)
	}
	if got := decayBySteps("critical", 2, "error"); got != "error" {
		t.Fatalf("active floor: %s", got)
	}
	if got := decayBySteps("error", 2, "info"); got != "info" {
		t.Fatalf("partial 60d: %s", got)
	}
	if got := decayBySteps("warning", 2, "error"); got != "info" {
		t.Fatalf("floor must not upgrade warning: %s", got)
	}
	if got := decayBySteps("error", 1, "error"); got != "error" {
		t.Fatalf("error floor stays: %s", got)
	}
}

func TestApplySeverityDecayImagePull(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	it := Issue{
		Severity: "critical", Category: "pod", Kind: "Pod",
		Namespace: "default", Name: "web", Reason: "ImagePullBackOff",
	}
	stampIssueSince(&it, now.Add(-61*24*time.Hour), now)
	items := []Issue{it}
	applySeverityDecay(items, now, false)
	if items[0].Severity != "warning" || items[0].BaseSeverity != "critical" {
		t.Fatalf("60d imagepull: %+v", items[0])
	}
	if !hasEvidence(items[0].Evidence, "severityNote:") || !hasEvidence(items[0].Evidence, "critical → warning") {
		t.Fatalf("note: %v", items[0].Evidence)
	}
}

func TestApplySeverityDecayCrashLoopFloor(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	it := Issue{
		Severity: "critical", Category: "pod", Kind: "Pod",
		Namespace: "app", Name: "api", Reason: "CrashLoopBackOff",
	}
	stampIssueSince(&it, now.Add(-61*24*time.Hour), now)
	items := []Issue{it}
	applySeverityDecay(items, now, false)
	if items[0].Severity != "error" {
		t.Fatalf("active crashloop should floor at error: %+v", items[0])
	}
}

func TestApplySeverityDecaySystemNS(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	it := Issue{
		Severity: "critical", Category: "pod", Kind: "Pod",
		Namespace: "kube-system", Name: "coredns", Reason: "ImagePullBackOff",
	}
	stampIssueSince(&it, now.Add(-61*24*time.Hour), now)
	items := []Issue{it}
	applySeverityDecay(items, now, false)
	if items[0].Severity != "error" {
		t.Fatalf("system ns max 1 / floor error: %+v", items[0])
	}
}

func TestApplySeverityDecayNoSince(t *testing.T) {
	now := time.Now()
	it := Issue{Severity: "critical", Category: "workload", Kind: "StatefulSet", Name: "db", Reason: "NotReady"}
	items := []Issue{it}
	applySeverityDecay(items, now, false)
	if items[0].Severity != "critical" {
		t.Fatalf("missing sinceAt must not decay: %+v", items[0])
	}
}

func TestApplySeverityDecayNever(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	cpu := Issue{Severity: "warning", Category: "node", Kind: "Node", Name: "n1", Reason: "CPUHigh"}
	stampIssueSince(&cpu, now.Add(-90*24*time.Hour), now)
	cfg := Issue{Severity: "critical", Category: "kubeconfig", Kind: "Kubeconfig", Name: "prod", Reason: "Unreachable"}
	stampIssueSince(&cfg, now.Add(-90*24*time.Hour), now)
	rst := Issue{Severity: "warning", Category: "pod", Kind: "Pod", Name: "web", Reason: "Restarts"}
	stampIssueSince(&rst, now.Add(-90*24*time.Hour), now)
	press := Issue{Severity: "error", Category: "node", Kind: "Node", Name: "n1", Reason: string(corev1.NodeDiskPressure)}
	stampIssueSince(&press, now.Add(-90*24*time.Hour), now)
	items := []Issue{cpu, cfg, rst, press}
	applySeverityDecay(items, now, false)
	if items[0].Severity != "warning" || items[1].Severity != "critical" || items[2].Severity != "warning" || items[3].Severity != "error" {
		t.Fatalf("never decay: %+v", items)
	}
}

func TestApplySeverityDecayNodeOutage(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	it := Issue{Severity: "critical", Category: "node", Kind: "Node", Name: "n1", Reason: "NotReady"}
	stampIssueSince(&it, now.Add(-61*24*time.Hour), now)
	hold := []Issue{it}
	applySeverityDecay(hold, now, true)
	if hold[0].Severity != "critical" {
		t.Fatalf("cluster outage must not decay node: %+v", hold[0])
	}
	solo := []Issue{it}
	applySeverityDecay(solo, now, false)
	if solo[0].Severity != "warning" {
		t.Fatalf("single stale node should decay: %+v", solo[0])
	}
}

func TestClusterNodeOutage(t *testing.T) {
	ready := func(name, status string) corev1.Node {
		return corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionStatus(status)},
			}},
		}
	}
	if clusterNodeOutage(nil) {
		t.Fatal("empty")
	}
	if clusterNodeOutage([]corev1.Node{ready("a", "True"), ready("b", "False")}) == false {
		t.Fatal("1/2 is 50%, treat as outage")
	}
	if clusterNodeOutage([]corev1.Node{ready("a", "True"), ready("b", "True"), ready("c", "False")}) {
		t.Fatal("1/3 should decay")
	}
	if !clusterNodeOutage([]corev1.Node{ready("a", "False"), ready("b", "Unknown")}) {
		t.Fatal("no ready nodes")
	}
}

func TestIsSystemNamespace(t *testing.T) {
	if !isSystemNamespace("kube-system") || !isSystemNamespace("cattle-system") || !isSystemNamespace("calico-apiserver") {
		t.Fatal("expected system")
	}
	if isSystemNamespace("default") || isSystemNamespace("monitoring") {
		t.Fatal("app ns")
	}
}

func TestApplySeverityDecaySystemWarningNotUpgrade(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	it := Issue{
		Severity: "warning", Category: "workload", Kind: "DaemonSet",
		Namespace: "kube-system", Name: "agent", Reason: "Misscheduled",
	}
	stampIssueSince(&it, now.Add(-61*24*time.Hour), now)
	items := []Issue{it}
	applySeverityDecay(items, now, false)
	if items[0].Severity != "info" {
		t.Fatalf("system warning should decay toward info, not up to error: %+v", items[0])
	}
}

func TestApplySeverityDecay30Days(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	it := Issue{Severity: "critical", Category: "pod", Name: "p", Reason: "ImagePullBackOff"}
	stampIssueSince(&it, now.Add(-31*24*time.Hour), now)
	items := []Issue{it}
	applySeverityDecay(items, now, false)
	if items[0].Severity != "error" {
		t.Fatalf("30d: %+v", items[0])
	}
	if !strings.Contains(items[0].Evidence[len(items[0].Evidence)-1], "critical → error") {
		t.Fatalf("note: %v", items[0].Evidence)
	}
}
