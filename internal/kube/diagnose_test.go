package kube

import (
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShouldScanNodes(t *testing.T) {
	if shouldScanNodes("project") {
		t.Fatal("project kubeconfig must skip nodes")
	}
	if !shouldScanNodes("cluster") {
		t.Fatal("cluster should scan nodes")
	}
	if !shouldScanNodes("") {
		t.Fatal("unknown perm still tries nodes")
	}
}

func TestDiagnoseNode(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	created := metav1.NewTime(now.Add(-2 * time.Hour))

	ready := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1", CreationTimestamp: created},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}
	if got := diagnoseNode(ready, nodeUsage{}, now); len(got) != 0 {
		t.Fatalf("ready node: %+v", got)
	}

	notReady := ready
	notReady.Name = "n2"
	notReady.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.8"}}
	notReady.Status.Conditions = []corev1.NodeCondition{
		{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionFalse,
			Reason:             "KubeletNotReady",
			Message:            "down",
			LastTransitionTime: metav1.NewTime(now.Add(-12 * time.Minute)),
			LastHeartbeatTime:  metav1.NewTime(now.Add(-12 * time.Minute)),
		},
	}
	got := diagnoseNode(notReady, nodeUsage{}, now)
	if len(got) != 1 || got[0].Severity != "critical" || got[0].Reason != "NotReady" {
		t.Fatalf("not ready: %+v", got)
	}
	if got[0].Since != "12m" {
		t.Fatalf("not ready since: %q", got[0].Since)
	}
	if got[0].NodeIP != "10.0.0.8" {
		t.Fatalf("not ready nodeIP: %q", got[0].NodeIP)
	}
	if !hasEvidence(got[0].Evidence, "internalIP=10.0.0.8") || !hasEvidence(got[0].Evidence, "Ready=False") {
		t.Fatalf("not ready evidence: %v", got[0].Evidence)
	}

	unknown := ready
	unknown.Name = "n3"
	unknown.Status.Conditions = nil
	got = diagnoseNode(unknown, nodeUsage{}, now)
	if len(got) != 1 || got[0].Reason != "Unknown" || got[0].Severity != "critical" {
		t.Fatalf("unknown: %+v", got)
	}

	pressure := ready
	pressure.Name = "n4"
	pressure.Status.Conditions = append(pressure.Status.Conditions, corev1.NodeCondition{
		Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Reason: "DiskPressure", Message: "full",
	})
	got = diagnoseNode(pressure, nodeUsage{}, now)
	if len(got) != 1 || got[0].Severity != "warning" || got[0].Reason != string(corev1.NodeDiskPressure) {
		t.Fatalf("disk pressure: %+v", got)
	}

	high := nodeUsage{valid: true, cpu: 3.7, mem: 1e9}
	got = diagnoseNode(ready, high, now)
	cpu := findReason(got, "CPUHigh")
	if cpu == nil {
		t.Fatalf("expected CPUHigh, got %+v", got)
	}
	if !strings.Contains(cpu.Message, "3.70") && !strings.Contains(cpu.Message, "93%") && !strings.Contains(cpu.Message, "92%") {
		t.Fatalf("CPUHigh should include used/percent, got %q", cpu.Message)
	}
	if !hasEvidence(cpu.Evidence, "cpu=") {
		t.Fatalf("CPUHigh evidence: %v", cpu.Evidence)
	}
	if hasReason(got, "MemoryHigh") {
		t.Fatalf("mem 1Gi/8Gi should not warn: %+v", got)
	}

	low := nodeUsage{valid: true, cpu: 3.5, mem: 1e9} // 87.5%
	got = diagnoseNode(ready, low, now)
	if len(got) != 0 {
		t.Fatalf("below threshold: %+v", got)
	}
}

func TestDiagnosePod(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)

	succeeded := podAt("ok", corev1.PodSucceeded, now.Add(-time.Hour))
	if got := diagnosePod(succeeded, now); got != nil {
		t.Fatalf("succeeded: %+v", got)
	}

	crash := podAt("crash", corev1.PodRunning, now.Add(-time.Hour))
	crash.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "app",
		RestartCount: 12,
		Ready:        false,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"},
		},
	}}
	crash.Spec.NodeName = "n1"
	crash.Status.HostIP = "10.0.0.8"
	crash.UID = "uid-1"
	crash.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{
		Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(-time.Minute)),
	}
	got := diagnosePod(crash, now)
	if len(got) != 1 || got[0].Severity != "critical" || got[0].Reason != "CrashLoopBackOff" {
		t.Fatalf("crashloop: %+v", got)
	}
	if got[0].Node != "n1" || got[0].NodeIP != "10.0.0.8" {
		t.Fatalf("crash node: name=%q ip=%q", got[0].Node, got[0].NodeIP)
	}
	if !hasEvidence(got[0].Evidence, "node=n1 ip=10.0.0.8") {
		t.Fatalf("crash node evidence: %v", got[0].Evidence)
	}
	if got[0].Container != "app" || got[0].LogPath != "/var/log/pods/default_crash_uid-1/app/11.log" {
		t.Fatalf("log path: container=%q path=%q", got[0].Container, got[0].LogPath)
	}
	if !strings.HasPrefix(got[0].LogSource, "previous") {
		t.Fatalf("crash should use previous logs: %q", got[0].LogSource)
	}
	if !hasEvidence(got[0].Evidence, "waiting CrashLoopBackOff") || !hasEvidence(got[0].Evidence, "exit=137") {
		t.Fatalf("crash evidence: %v", got[0].Evidence)
	}

	pull := podAt("pull", corev1.PodPending, now.Add(-time.Hour))
	pull.Status.ContainerStatuses = []corev1.ContainerStatus{{
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "not found"},
		},
	}}
	got = diagnosePod(pull, now)
	if len(got) != 1 || got[0].Reason != "ImagePullBackOff" || got[0].Severity != "critical" {
		t.Fatalf("image pull: %+v", got)
	}

	oom := podAt("oom", corev1.PodRunning, now.Add(-time.Hour))
	oom.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Ready: false,
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", Message: "oom"},
		},
	}}
	got = diagnosePod(oom, now)
	if len(got) != 1 || got[0].Reason != "OOMKilled" {
		t.Fatalf("oom: %+v", got)
	}

	failed := podAt("fail", corev1.PodFailed, now.Add(-time.Hour))
	got = diagnosePod(failed, now)
	if len(got) != 1 || got[0].Severity != "critical" || got[0].Reason != "Failed" {
		t.Fatalf("failed: %+v", got)
	}

	evicted := podAt("evict", corev1.PodFailed, now.Add(-time.Hour))
	evicted.Status.Reason = "Evicted"
	evicted.Status.Message = "The node was low on resource: memory."
	evicted.Spec.NodeName = "n1"
	evicted.UID = "uid-e"
	evicted.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "rancher", RestartCount: 30, Ready: false,
	}}
	got = diagnosePod(evicted, now)
	if got != nil {
		t.Fatalf("evicted leftover should be ignored: %+v", got)
	}

	unknown := podAt("unk", corev1.PodUnknown, now.Add(-time.Hour))
	got = diagnosePod(unknown, now)
	if len(got) != 1 || got[0].Severity != "warning" || got[0].Reason != "Unknown" {
		t.Fatalf("unknown: %+v", got)
	}

	notReady := podAt("nr", corev1.PodRunning, now.Add(-time.Hour))
	notReady.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "a", Ready: true},
		{Name: "b", Ready: false},
	}
	got = diagnosePod(notReady, now)
	if len(got) != 1 || got[0].Severity != "warning" || got[0].Reason != "NotReady" {
		t.Fatalf("not ready: %+v", got)
	}
	if !strings.Contains(got[0].Message, "1/2") {
		t.Fatalf("ready message: %s", got[0].Message)
	}

	young := podAt("young", corev1.PodPending, now.Add(-time.Minute))
	if got = diagnosePod(young, now); got != nil {
		t.Fatalf("young pending: %+v", got)
	}

	oldPending := podAt("stuck", corev1.PodPending, now.Add(-3*time.Minute))
	oldPending.Status.Conditions = []corev1.PodCondition{{
		Type:    corev1.PodScheduled,
		Status:  corev1.ConditionFalse,
		Reason:  "Unschedulable",
		Message: "0/3 nodes",
	}}
	got = diagnosePod(oldPending, now)
	if len(got) != 1 || got[0].Severity != "warning" || got[0].Reason != "Unschedulable" {
		t.Fatalf("pending: %+v", got)
	}

	restarts := podAt("rst", corev1.PodRunning, now.Add(-time.Hour))
	restarts.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 5}}
	got = diagnosePod(restarts, now)
	if len(got) != 1 || got[0].Reason != "Restarts" || got[0].Severity != "warning" {
		t.Fatalf("restarts: %+v", got)
	}
}

func TestDiagnoseWorkloads(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	created := metav1.NewTime(now.Add(-time.Hour))
	three := int32(3)
	zero := int32(0)

	ok := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "ns", CreationTimestamp: created},
		Spec:       appsv1.DeploymentSpec{Replicas: &three},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 3},
	}
	if got := diagnoseDeployment(ok, now); got != nil {
		t.Fatalf("healthy deploy: %+v", got)
	}

	none := ok
	none.Name = "none"
	none.Status.ReadyReplicas = 0
	none.Status.UnavailableReplicas = 3
	none.Status.Conditions = []appsv1.DeploymentCondition{{
		Type:               appsv1.DeploymentProgressing,
		Status:             corev1.ConditionFalse,
		Reason:             "ProgressDeadlineExceeded",
		Message:            "timed out",
		LastTransitionTime: metav1.NewTime(now.Add(-8 * time.Minute)),
	}}
	got := diagnoseDeployment(none, now)
	if len(got) != 1 || got[0].Severity != "critical" || got[0].Reason != "NotReady" {
		t.Fatalf("0 ready: %+v", got)
	}
	if got[0].Since != "8m" {
		t.Fatalf("deploy since: %q", got[0].Since)
	}
	if !hasEvidence(got[0].Evidence, "desired=3") || !hasEvidence(got[0].Evidence, "ProgressDeadlineExceeded") {
		t.Fatalf("deploy evidence: %v", got[0].Evidence)
	}

	partial := ok
	partial.Name = "partial"
	partial.Status.ReadyReplicas = 2
	got = diagnoseDeployment(partial, now)
	if len(got) != 1 || got[0].Severity != "warning" {
		t.Fatalf("partial: %+v", got)
	}

	scaled := ok
	scaled.Spec.Replicas = &zero
	scaled.Status.ReadyReplicas = 0
	if got = diagnoseDeployment(scaled, now); got != nil {
		t.Fatalf("scaled to 0: %+v", got)
	}

	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns", CreationTimestamp: created},
		Spec:       appsv1.StatefulSetSpec{Replicas: &three},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	got = diagnoseStatefulSet(sts, now)
	if len(got) != 1 || got[0].Kind != "StatefulSet" || got[0].Severity != "warning" {
		t.Fatalf("sts: %+v", got)
	}

	ds := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "kube-system", CreationTimestamp: created},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 3,
			NumberReady:            0,
			NumberMisscheduled:     1,
		},
	}
	got = diagnoseDaemonSet(ds, now)
	if len(got) != 1 || got[0].Severity != "critical" || !strings.Contains(got[0].Message, "misscheduled") {
		t.Fatalf("ds critical: %+v", got)
	}

	dsOKReady := ds
	dsOKReady.Status.NumberReady = 3
	got = diagnoseDaemonSet(dsOKReady, now)
	if len(got) != 1 || got[0].Reason != "Misscheduled" || got[0].Severity != "warning" {
		t.Fatalf("ds misscheduled: %+v", got)
	}
}

func TestDiagnoseEventAndSort(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	e := corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute))},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web"},
		Reason:         "FailedScheduling",
		Message:        "0/3 nodes available",
		Type:           corev1.EventTypeWarning,
		Count:          4,
		LastTimestamp:  metav1.NewTime(now.Add(-time.Minute)),
	}
	got := diagnoseEvent(e, now)
	if got.Category != "event" || got.Severity != "warning" || got.Kind != "Pod" || got.Name != "web" {
		t.Fatalf("event: %+v", got)
	}
	if !strings.Contains(got.Message, "x4") {
		t.Fatalf("count prefix: %s", got.Message)
	}

	probe := e
	probe.Reason = "Unhealthy"
	probe.Message = "Readiness probe failed: Get http://10.0.0.8:8080/ready"
	probe.Source = corev1.EventSource{Component: "kubelet", Host: "n1"}
	probe.InvolvedObject.FieldPath = "spec.containers{jenkins}"
	probe.InvolvedObject.Namespace = "ci"
	probe.InvolvedObject.Name = "jenkins-0"
	got = diagnoseEvent(probe, now)
	if got.Node != "n1" || got.Container != "jenkins" || got.Reporter != "kubelet" || got.Namespace != "ci" {
		t.Fatalf("probe loc: node=%q container=%q reporter=%q ns=%q", got.Node, got.Container, got.Reporter, got.Namespace)
	}
	if !hasEvidence(got.Evidence, "出处: Kubernetes Warning 事件") || !hasEvidence(got.Evidence, "Event.source.host") {
		t.Fatalf("probe evidence: %v", got.Evidence)
	}

	enriched := []Issue{got}
	enrichLocations(enriched, map[string]resourceLoc{"pod|ci|jenkins-0": {Node: "n1", NodeIP: "10.0.0.8"}}, map[string]string{"n1": "10.0.0.8"})
	if enriched[0].NodeIP != "10.0.0.8" {
		t.Fatalf("enriched ip: %q", enriched[0].NodeIP)
	}

	podIssue := Issue{Severity: "critical", Category: "pod", Kind: "Pod", Namespace: "default", Name: "web", Reason: "CrashLoopBackOff"}
	merged, attached := attachEvents([]Issue{podIssue}, []corev1.Event{e}, now)
	if attached != 1 {
		t.Fatalf("attached=%d", attached)
	}
	if len(merged) != 1 {
		t.Fatalf("event should hang on pod, not duplicate: %d", len(merged))
	}
	if !hasEvidence(merged[0].Evidence, "Warning FailedScheduling") {
		t.Fatalf("attached evidence: %v", merged[0].Evidence)
	}

	orphan := e
	orphan.InvolvedObject.Name = "other"
	merged, attached = attachEvents([]Issue{podIssue}, []corev1.Event{orphan}, now)
	if attached != 0 || len(merged) != 2 || merged[1].Category != "event" {
		t.Fatalf("orphan: attached=%d n=%d cat=%s", attached, len(merged), merged[len(merged)-1].Category)
	}

	items := []Issue{
		{Severity: "warning", Category: "pod", Name: "a"},
		{Severity: "critical", Category: "node", Name: "n"},
		{Severity: "critical", Category: "pod", Name: "b"},
	}
	sortIssues(items)
	if items[0].Category != "node" || items[1].Category != "pod" || items[1].Name != "b" || items[2].Severity != "warning" {
		t.Fatalf("sort: %+v", items)
	}
}

func podAt(name string, phase corev1.PodPhase, created time.Time) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(created),
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func hasReason(items []Issue, reason string) bool {
	return findReason(items, reason) != nil
}

func findReason(items []Issue, reason string) *Issue {
	for i := range items {
		if items[i].Reason == reason {
			return &items[i]
		}
	}
	return nil
}

func hasEvidence(ev []string, sub string) bool {
	for _, s := range ev {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestShouldFetchAndPickLogs(t *testing.T) {
	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName", "Evicted"} {
		if shouldFetchPodLogs(Issue{Category: "pod", Severity: "critical", Namespace: "ns", Name: "p", Reason: reason}) {
			t.Fatalf("%s has no container logs", reason)
		}
	}
	if !shouldFetchPodLogs(Issue{Category: "pod", Severity: "critical", Namespace: "ns", Name: "p", Reason: "CrashLoopBackOff"}) {
		t.Fatal("crashloop should fetch")
	}
	issues := []Issue{
		{Category: "pod", Severity: "critical", Namespace: "ns", Name: "a", Reason: "Failed"},
		{Category: "pod", Severity: "critical", Namespace: "ns", Name: "b", Reason: "CrashLoopBackOff"},
		{Category: "pod", Severity: "warning", Namespace: "ns", Name: "c", Reason: "NotReady"},
		{Category: "pod", Severity: "critical", Namespace: "ns", Name: "d", Reason: "OOMKilled"},
	}
	got := pickLogIssueIndexes(issues)
	if len(got) != 3 || issues[got[0]].Reason != "CrashLoopBackOff" || issues[got[1]].Reason != "OOMKilled" {
		t.Fatalf("priority: %v", got)
	}
}

func TestExcerptLogs(t *testing.T) {
	raw := strings.Join([]string{
		"boot ok",
		"listen :8080",
		"WARN slow query",
		"ERROR connection refused",
		"retrying",
		"INFO idle",
		"FATAL panic: nil pointer",
		"goroutine 1",
	}, "\n")
	got := excerptLogs(raw)
	if !hasEvidence(got, "ERROR connection refused") || !hasEvidence(got, "FATAL panic") {
		t.Fatalf("key lines: %v", got)
	}
	if !hasEvidence(got, "retrying") || !hasEvidence(got, "goroutine 1") {
		t.Fatalf("need ±1 context: %v", got)
	}
	if hasEvidence(got, "boot ok") {
		t.Fatalf("should drop unrelated head: %v", got)
	}
	plain := excerptLogs("a\nb\nc\nd\ne\nf\ng\nh\ni\n")
	if len(plain) > diagnoseLogExcerptMax {
		t.Fatalf("plain cap: %v", plain)
	}
	if plain[len(plain)-1] != "i" {
		t.Fatalf("plain should keep tail: %v", plain)
	}
}

func TestKeepTopIssuesPrefersCritical(t *testing.T) {
	items := []Issue{
		{Severity: "warning", Category: "pod", Name: "w1", Reason: "NotReady"},
		{Severity: "warning", Category: "pod", Name: "w2", Reason: "NotReady"},
		{Severity: "critical", Category: "pod", Name: "crash", Reason: "CrashLoopBackOff"},
	}
	if !keepTopIssues(&items, 2) || len(items) != 2 || items[0].Name != "crash" {
		t.Fatalf("should keep crashloop: %+v", items)
	}
}

func TestDiagnoseListOpts(t *testing.T) {
	first := diagnoseListOpts("", 80, "type=Warning")
	if first.ResourceVersion != "0" || first.Continue != "" || first.FieldSelector != "type=Warning" {
		t.Fatalf("first page: %+v", first)
	}
	next := diagnoseListOpts("tok", 80, "type=Warning")
	if next.ResourceVersion != "" || next.Continue != "tok" {
		t.Fatalf("continue page must not set RV: %+v", next)
	}
}

func TestAppendWarningEvidence(t *testing.T) {
	ev := make([]string, diagnoseEvidenceMax)
	for i := range ev {
		ev[i] = fmt.Sprintf("fact-%d", i)
	}
	got := appendWarningEvidence(ev, "Warning BackOff 1m: restart")
	if !hasEvidence(got, "Warning BackOff") {
		t.Fatalf("should keep warning: %v", got)
	}
	if len(got) != diagnoseEvidenceMax {
		t.Fatalf("len=%d", len(got))
	}
	capped := appendWarningEvidence([]string{"Warning A", "Warning B", "Warning C", "Warning D", "Warning E", "fact"}, "Warning Extra")
	if hasEvidence(capped, "Warning Extra") {
		t.Fatalf("should stop at %d warnings: %v", diagnoseEventPerIssue, capped)
	}
}

func TestDoDiagnoseListFallback(t *testing.T) {
	var calls []metav1.ListOptions
	miss := 0
	opt := DiagnoseOptions{OnCacheMiss: func() { miss++ }}
	items, next, err := doDiagnoseList(opt, "", 10, "status.phase!=Succeeded", func(o metav1.ListOptions) ([]string, string, error) {
		calls = append(calls, o)
		if o.ResourceVersion == "0" {
			return nil, "", fmt.Errorf("resourceVersion: invalid")
		}
		if o.FieldSelector != "" {
			return nil, "", fmt.Errorf("field label not supported")
		}
		return []string{"ok"}, "", nil
	})
	if err != nil || next != "" || len(items) != 1 || items[0] != "ok" {
		t.Fatalf("got %v %q %v", items, next, err)
	}
	if len(calls) != 3 || miss != 1 {
		t.Fatalf("calls=%d miss=%d", len(calls), miss)
	}
}
