package inspect

import "testing"

func TestFastAlertFP(t *testing.T) {
	if !fastAlertFP("pod|Pod|ns|web|CrashLoopBackOff") {
		t.Fatal("pod alerts are rechecked")
	}
	if !fastAlertFP("pod|Pod|ns|web|ErrImagePull") {
		t.Fatal("image pull is rechecked")
	}
	if !fastAlertFP("node|Node||n1|NotReady") || !fastAlertFP("node|Node||n1|Unknown") {
		t.Fatal("node conditions are rechecked")
	}
	if fastAlertFP("node|Node||n1|CPUHigh") || fastAlertFP("node|Node||n1|MemoryHigh") {
		t.Fatal("node metrics wait for the full diagnose")
	}
	if fastAlertFP("storage|StorageClass||nfs|NFSHighUsage") || fastAlertFP("storage|Pod|ns|big|LargeEphemeral") {
		t.Fatal("NFS and large pods wait for the full diagnose")
	}
	if !fastAlertFP("kubeconfig|Kubeconfig||prod|Unreachable") {
		t.Fatal("connectivity is rechecked")
	}
	if fastAlertFP("workload|Job|ns|batch|NotReady") {
		t.Fatal("jobs are not a fast workload target")
	}
}

func TestControllerReadyDoesNotUseRolloutGrace(t *testing.T) {
	if !controllerReady(3, 3, 0) {
		t.Fatal("ready replicas should close")
	}
	if controllerReady(3, 2, 0) {
		t.Fatal("partial ready must stay open")
	}
	if controllerReady(3, 3, 1) {
		t.Fatal("misscheduled must stay open")
	}
	if !controllerReady(0, 0, 0) {
		t.Fatal("scaled to zero is ready")
	}
	if podMayClose(ctrlNotReady) || !podMayClose(ctrlReady) || !podMayClose(ctrlGone) || podMayClose(ctrlUnknown) {
		t.Fatal("pod alerts close only when the controller is ready or gone")
	}
}

func TestPodNameMatchesOwningController(t *testing.T) {
	ctrls := []namedCtrl{
		{kind: "Deployment", name: "web"},
		{kind: "Deployment", name: "web-api"},
		{kind: "StatefulSet", name: "jenkins"},
		{kind: "DaemonSet", name: "agent"},
		{kind: "DaemonSet", name: "jenkins"},
	}
	got, ok := bestController("web-api-7d9f8b6c4d-xk2pq", ctrls)
	if !ok || got.kind != "Deployment" || got.name != "web-api" {
		t.Fatalf("deployment match %+v ok=%v", got, ok)
	}
	got, ok = bestController("web-7d9f8b6c4d-xk2pq", ctrls)
	if !ok || got.name != "web" {
		t.Fatalf("shorter deployment %+v", got)
	}
	got, ok = bestController("jenkins-0", ctrls)
	if !ok || got.kind != "StatefulSet" || got.name != "jenkins" {
		t.Fatalf("statefulset ordinal must not match a daemonset: %+v", got)
	}
	if _, ok := bestController("agent-xk2pq", ctrls); ok {
		t.Fatal("a missing pod must not be guessed onto a daemonset")
	}
	if _, ok := bestController("jenkins-0", []namedCtrl{{kind: "Deployment", name: "jenkins"}}); ok {
		t.Fatal("ordinal must not match a deployment")
	}
	if _, ok := bestController("replaced-pod", ctrls); ok {
		t.Fatal("unmatched pod stays unresolved")
	}
}

func TestNoteWorkloadUnreadyDoesNotLookHealthy(t *testing.T) {
	key := ctrlKey{kind: "Deployment", ns: "ns", name: "web"}
	fp := "workload|Deployment|ns|web|NotReady"
	rc := &rechecker{
		sentCanon: map[string]struct{}{fp: {}},
		workloads: map[ctrlKey][]string{key: {fp}},
		scope:     map[string]struct{}{},
		stillSeen: map[string]struct{}{},
	}
	rc.noteWorkload(key, ctrlNotReady, ctrlCounts{desired: 2, ready: 1, miss: 0, ok: true})
	if _, ok := rc.scope[fp]; !ok {
		t.Fatal("unready workload was rechecked and stays in scope")
	}
	if len(rc.still) != 1 || rc.still[0].Reason != "NotReady" {
		t.Fatalf("still=%+v", rc.still)
	}

	ready := &rechecker{
		sentCanon: map[string]struct{}{fp: {}},
		workloads: map[ctrlKey][]string{key: {fp}},
		scope:     map[string]struct{}{},
		stillSeen: map[string]struct{}{},
	}
	ready.noteWorkload(key, ctrlReady, ctrlCounts{desired: 2, ready: 2, ok: true})
	if _, ok := ready.scope[fp]; !ok || len(ready.still) != 0 {
		t.Fatalf("ready workload should be closable, scope=%v still=%+v", ready.scope, ready.still)
	}
}
