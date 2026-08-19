package kube

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTopRestartHitsLastStateAndEvents(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := crashPod("web-0", now)
	p.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt = metav1.NewTime(now.Add(-time.Minute))
	events := []corev1.Event{
		podRestartEvent("web-0", "BackOff", now.Add(-8*time.Minute), 4),
		podRestartEvent("web-0", "Killing", now.Add(-12*time.Minute), 1),
	}
	hits := topRestartHits(collectPodRestartHits(p, events, now), now)
	if len(hits) != 3 {
		t.Fatalf("want 3 hits, got %+v", hits)
	}
	lines := formatRestartHits(hits, now)
	if !hasEvidence(lines, "restart[1]") || !hasEvidence(lines, "OOMKilled") || !hasEvidence(lines, "status.lastState") {
		t.Fatalf("lastState first: %v", lines)
	}
	if !hasEvidence(lines, "restart[2]") || !hasEvidence(lines, "BackOff") || !hasEvidence(lines, "×4") {
		t.Fatalf("event 2: %v", lines)
	}
	if !hasEvidence(lines, "restart[3]") || !hasEvidence(lines, "Killing") {
		t.Fatalf("event 3: %v", lines)
	}
	if restartNote(len(hits)) != "" {
		t.Fatalf("3 hits should not note: %q", restartNote(len(hits)))
	}
}

func TestTopRestartHitsDedupLastStateAndBackOff(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := crashPod("web-0", now)
	events := []corev1.Event{
		podRestartEvent("web-0", "BackOff", now.Add(-90*time.Second), 12),
	}
	hits := topRestartHits(collectPodRestartHits(p, events, now), now)
	if len(hits) != 1 {
		t.Fatalf("should collapse BackOff with lastState, got %+v", hits)
	}
	if hits[0].Source != "status.lastState" || hits[0].Reason != "OOMKilled" {
		t.Fatalf("prefer lastState: %+v", hits[0])
	}
}

func TestTopRestartHitsOnlyLastStateNote(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := crashPod("web-0", now)
	hits := topRestartHits(collectPodRestartHits(p, nil, now), now)
	if len(hits) != 1 {
		t.Fatalf("want 1, got %+v", hits)
	}
	ev := applyRestartEvidence([]string{"phase=Running"}, hits, "", now)
	if !hasEvidence(ev, "restart[1]") || !hasEvidence(ev, "restartNote:") || !hasEvidence(ev, "只确认 1 条") {
		t.Fatalf("note missing: %v", ev)
	}
	if ev[0] != formatRestartHit(hits[0], 1, now) {
		t.Fatalf("restart should be first: %v", ev)
	}
}

func TestAttachRestartHistoryPodAndWorkloadNotReady(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := crashPod("web-7d9f", now)
	p.Labels = map[string]string{"app": "web"}
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour))},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
		},
	}
	ref, ok := newWorkloadRef("Deployment", dep.Namespace, dep.Name, dep.CreationTimestamp.Time, dep.Spec.Selector)
	if !ok {
		t.Fatal("selector")
	}
	podIssues := diagnosePod(p, now)
	wl := replicaIssues("Deployment", "default", "web", 2, 0, 0, dep.CreationTimestamp.Time, now.Add(-5*time.Minute), now, []string{"replicas desired=2 ready=0"})
	events := []corev1.Event{
		podRestartEvent("web-7d9f", "Killing", now.Add(-20*time.Minute), 1),
	}
	out := attachRestartHistory(append(podIssues, wl...), []corev1.Pod{p}, []workloadRef{ref}, events, now)
	pod := findIssue(out, "pod", "CrashLoopBackOff")
	if pod == nil || !hasEvidence(pod.Evidence, "restart[1]") || !hasEvidence(pod.Evidence, "OOMKilled") {
		t.Fatalf("pod evidence: %+v", pod)
	}
	notReady := findIssue(out, "workload", "NotReady")
	if notReady == nil {
		t.Fatalf("want NotReady, got %+v", out)
	}
	if !hasEvidence(notReady.Evidence, "restart[1]") || !hasEvidence(notReady.Evidence, "selector=app=web") {
		t.Fatalf("workload evidence: %v", notReady.Evidence)
	}
	if findIssue(out, "workload", "Restarts") != nil {
		t.Fatalf("should not add extra Restarts when NotReady exists: %+v", out)
	}
}

func TestAttachRestartHistoryReadyWorkloadRestarts(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := crashPod("web-7d9f", now)
	p.Labels = map[string]string{"app": "web"}
	ref, ok := newWorkloadRef("Deployment", "default", "web", now.Add(-time.Hour), &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}})
	if !ok {
		t.Fatal("selector")
	}
	out := attachRestartHistory(nil, []corev1.Pod{p}, []workloadRef{ref}, nil, now)
	got := findIssue(out, "workload", "Restarts")
	if got == nil {
		t.Fatalf("want workload Restarts, got %+v", out)
	}
	if got.Severity != "warning" || !strings.Contains(got.Message, "OOMKilled") {
		t.Fatalf("message: %+v", got)
	}
	if !hasEvidence(got.Evidence, "restart[1]") || !hasEvidence(got.Evidence, "selector=app=web") {
		t.Fatalf("evidence: %v", got.Evidence)
	}
}

func TestAttachRestartHistorySkipHealthy(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := podAt("web-ok", corev1.PodRunning, now.Add(-time.Hour))
	p.Labels = map[string]string{"app": "web"}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", Ready: true, RestartCount: 0,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Hour))}},
	}}
	ref, _ := newWorkloadRef("Deployment", "default", "web", now.Add(-time.Hour), &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}})
	out := attachRestartHistory(nil, []corev1.Pod{p}, []workloadRef{ref}, nil, now)
	if len(out) != 0 {
		t.Fatalf("healthy should stay empty: %+v", out)
	}
}

func TestAttachRestartHistorySkipRollout(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := crashPod("web-7d9f", now)
	p.Labels = map[string]string{"app": "web"}
	ref, ok := newWorkloadRef("Deployment", "default", "web", now.Add(-time.Hour), &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}})
	if !ok {
		t.Fatal("selector")
	}
	ref.RolloutRecent = true
	out := attachRestartHistory(nil, []corev1.Pod{p}, []workloadRef{ref}, nil, now)
	if findIssue(out, "workload", "Restarts") != nil {
		t.Fatalf("rollout should not add controller Restarts: %+v", out)
	}
}

func TestTopRestartHitsStaleIgnored(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := crashPod("web-0", now)
	p.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt = metav1.NewTime(now.Add(-3 * time.Hour))
	hits := topRestartHits(collectPodRestartHits(p, nil, now), now)
	if len(hits) != 0 {
		t.Fatalf("stale lastState should drop: %+v", hits)
	}
}

func TestAppendWarningEvidenceKeepsRestartLines(t *testing.T) {
	want := "restart[1] 1m ago pod=a OOMKilled exit=137 · status.lastState"
	ev := make([]string, diagnoseEvidenceMax)
	ev[0] = want
	ev[1] = "restartNote: 只确认 1 条"
	ev[2] = "selector=app=web"
	for i := 3; i < len(ev); i++ {
		ev[i] = "fact"
	}
	got := appendWarningEvidence(ev, "Warning Extra")
	if got[0] != want || !hasEvidence(got, "restartNote:") || !hasEvidence(got, "selector=app=web") {
		t.Fatalf("restart lines must stay: %v", got)
	}
	if !hasEvidence(got, "Warning Extra") {
		t.Fatalf("should still attach warning: %v", got)
	}
}

func TestAttachEventsSkipDuplicateRestartBackOff(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	it := Issue{
		Severity: "critical", Category: "pod", Kind: "Pod",
		Namespace: "default", Name: "web-0", Reason: "CrashLoopBackOff",
		Evidence: []string{"restart[1] 1m ago pod=web-0 container=app BackOff ×4 · event"},
	}
	e := podRestartEvent("web-0", "BackOff", now.Add(-time.Minute), 4)
	merged, attached := attachEvents([]Issue{it}, []corev1.Event{e}, now, nil)
	if attached != 1 {
		t.Fatalf("event is consumed by existing issue, attached=%d", attached)
	}
	if hasEvidence(merged[0].Evidence, "Warning BackOff") {
		t.Fatalf("should not duplicate BackOff: %v", merged[0].Evidence)
	}
}

func TestPrependRestartEvidenceKeepsFront(t *testing.T) {
	ev := make([]string, diagnoseEvidenceMax)
	for i := range ev {
		ev[i] = "fact"
	}
	got := prependRestartEvidence(ev, []string{"restart[1] 1m ago pod=a OOMKilled exit=137 · status.lastState"}, "restartNote: 只确认 1 条")
	if got[0] != "restart[1] 1m ago pod=a OOMKilled exit=137 · status.lastState" {
		t.Fatalf("front: %v", got)
	}
	if !hasEvidence(got, "restartNote:") {
		t.Fatalf("note: %v", got)
	}
	if len(got) != diagnoseEvidenceMax {
		t.Fatalf("len=%d", len(got))
	}
}

func crashPod(name string, now time.Time) corev1.Pod {
	p := podAt(name, corev1.PodRunning, now.Add(-time.Hour))
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "app",
		RestartCount: 12,
		Ready:        false,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"},
		},
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(-time.Minute)),
			},
		},
	}}
	return p
}

func podRestartEvent(pod, reason string, when time.Time, count int32) corev1.Event {
	return corev1.Event{
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: pod, FieldPath: "spec.containers{app}"},
		Reason:         reason,
		Message:        reason,
		Count:          count,
		LastTimestamp:  metav1.NewTime(when),
		Type:           corev1.EventTypeWarning,
	}
}

func findIssue(items []Issue, category, reason string) *Issue {
	for i := range items {
		if items[i].Category == category && items[i].Reason == reason {
			return &items[i]
		}
	}
	return nil
}
