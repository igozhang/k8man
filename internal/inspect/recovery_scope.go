package inspect

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gitee.com/k8man/internal/alert"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/kube/owner"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

// scopedOutcome is a recovery recheck that did not run a full diagnose.
// skipNotify means nothing was rechecked, so alert_sent must be left as-is.
type scopedOutcome struct {
	cluster    *model.Cluster
	issues     []kube.Issue
	gate       alert.RecoverGate
	round      int
	skipNotify bool
}

type ctrlResult int

const (
	ctrlUnknown ctrlResult = iota
	ctrlGone
	ctrlReady
	ctrlNotReady
)

type ctrlKey struct {
	kind, ns, name string
}

type podKey struct {
	ns, name string
}

type namedCtrl struct {
	kind, ns, name       string
	desired, ready, miss int32
}

// recoveryProbePlan reports whether this cluster needs API calls.
// drop is set when nothing is open. fast is false when only NFS / large-pod
// (or other non-rechecked) alerts remain; those wait for the 2h diagnose.
func (w *Worker) recoveryProbePlan(id string) (fast, drop bool) {
	if w == nil || w.Store == nil || id == "" {
		return false, false
	}
	sent, err := w.Store.ListAlertSent(id)
	if err != nil {
		log.Printf("recovery probe sent %s: %v", id, err)
		return true, false
	}
	if len(sent) == 0 {
		return false, true
	}
	for fp := range sent {
		if fastAlertFP(fp) {
			return true, false
		}
	}
	return false, false
}

func fastAlertFP(fp string) bool {
	category, kind, _, _, reason, ok := splitFingerprint(fp)
	if !ok {
		return false
	}
	switch category {
	case "kubeconfig":
		return reason == "Unreachable" || reason == "ClientError"
	case "pod":
		return true
	case "workload":
		switch kind {
		case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
			return true
		default:
			return false
		}
	case "node":
		return nodeFastReason(reason)
	default:
		return false
	}
}

func nodeFastReason(reason string) bool {
	switch reason {
	case "NotReady", "Unknown", "DiskPressure", "MemoryPressure", "PIDPressure", "NetworkUnavailable":
		return true
	default:
		return false
	}
}

func splitFingerprint(fp string) (category, kind, ns, name, reason string, ok bool) {
	parts := strings.Split(fp, "|")
	if len(parts) < 5 {
		return "", "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], strings.Join(parts[4:], "|"), true
}

// scopedRecovery rechecks only the objects behind open alerts.
// It does not write an inspection snapshot, a round file, or the 2h nextDue.
func (w *Worker) scopedRecovery(ctx context.Context, id string) (*scopedOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cl, err := w.Store.GetCluster(id, true)
	if err != nil {
		return nil, err
	}
	if cl == nil {
		return nil, ErrNotFound
	}
	sent, err := w.Store.ListAlertSent(id)
	if err != nil {
		return nil, err
	}
	if !sentHasFast(sent) {
		return &scopedOutcome{cluster: cl, skipNotify: true}, nil
	}

	// Do not bump the alert round. A full diagnose may still be waiting to push;
	// incrementing here would drop that message, and this recheck does not have its issues.
	round := 0
	if w.Alerts != nil {
		round = w.Alerts.CurrentRound(cl.ID)
	}
	if w.Hosts != nil {
		_, _ = w.Hosts.SyncIfChanged()
	}

	runCtx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	hosts := (*kube.HostsResolver)(nil)
	if w.Pool != nil {
		hosts = w.Pool.Hosts()
	}
	probe := kube.Probe(runCtx, cl.Kubeconfig, hosts)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	applyProbeStatus(cl, probe)
	if err := w.Store.UpdateClusterProbe(cl, strings.EqualFold(strings.TrimSpace(probe.Status), "ok")); err != nil {
		log.Printf("recovery probe update cluster %s: %v", cl.ID, err)
	}
	if !strings.EqualFold(strings.TrimSpace(probe.Status), "ok") {
		return &scopedOutcome{
			cluster: cl,
			issues:  []kube.Issue{kubeconfigIssue(cl, probe)},
			gate:    alert.RecoverGate{},
			round:   round,
		}, nil
	}

	rc := newRechecker(w, runCtx, sent)
	if w.Pool == nil {
		log.Printf("recovery probe %s: kube client pool missing", cl.ID)
		return rc.outcome(cl, round), nil
	}
	clients, err := w.Pool.Get(cl.ID, cl.Kubeconfig)
	if err != nil {
		log.Printf("recovery probe %s: client: %v", cl.ID, err)
		return rc.outcome(cl, round), nil
	}
	rc.clients = clients
	rc.recheck(w.now())
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := runCtx.Err(); err != nil {
		return nil, err
	}
	return rc.outcome(cl, round), nil
}

func sentHasFast(sent map[string]store.AlertSentRow) bool {
	for fp := range sent {
		if fastAlertFP(fp) {
			return true
		}
	}
	return false
}

func applyProbeStatus(cl *model.Cluster, probe kube.ProbeResult) {
	cl.Status = probe.Status
	cl.LastError = probe.LastError
	if !strings.EqualFold(strings.TrimSpace(probe.Status), "ok") {
		return
	}
	cl.Server = probe.Server
	cl.PermType = probe.PermType
	cl.Namespaces = probe.Namespaces
	cl.FrontProxy = probe.FrontProxy
	cl.KubeVersion = probe.KubeVersion
}

type rechecker struct {
	w         *Worker
	ctx       context.Context
	clients   *kube.Clients
	sent      map[string]store.AlertSentRow
	sentCanon map[string]struct{}
	pods      map[podKey][]string
	workloads map[ctrlKey][]string
	nodes     map[string][]string
	scope     map[string]struct{}
	still     []kube.Issue
	stillSeen map[string]struct{}
	ctrl      map[ctrlKey]ctrlResult
	nsCtrls   map[string][]namedCtrl
	nsErr     map[string]error
}

func newRechecker(w *Worker, ctx context.Context, sent map[string]store.AlertSentRow) *rechecker {
	rc := &rechecker{
		w:         w,
		ctx:       ctx,
		sent:      sent,
		sentCanon: map[string]struct{}{},
		pods:      map[podKey][]string{},
		workloads: map[ctrlKey][]string{},
		nodes:     map[string][]string{},
		scope:     map[string]struct{}{},
		stillSeen: map[string]struct{}{},
		ctrl:      map[ctrlKey]ctrlResult{},
		nsCtrls:   map[string][]namedCtrl{},
		nsErr:     map[string]error{},
	}
	for fp := range sent {
		canon := alert.CanonicalFingerprint(fp)
		rc.sentCanon[canon] = struct{}{}
		category, kind, ns, name, reason, ok := splitFingerprint(fp)
		if !ok {
			continue
		}
		switch category {
		case "kubeconfig":
			if reason == "Unreachable" || reason == "ClientError" {
				rc.scope[canon] = struct{}{}
			}
		case "pod":
			if name == "" {
				continue
			}
			key := podKey{ns: ns, name: name}
			rc.pods[key] = append(rc.pods[key], fp)
		case "workload":
			if name == "" || !workloadKind(kind) {
				continue
			}
			key := ctrlKey{kind: kind, ns: ns, name: name}
			rc.workloads[key] = append(rc.workloads[key], fp)
		case "node":
			if name == "" || !nodeFastReason(reason) {
				continue
			}
			rc.nodes[name] = append(rc.nodes[name], fp)
		}
	}
	return rc
}

func workloadKind(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		return true
	default:
		return false
	}
}

func (rc *rechecker) outcome(cl *model.Cluster, round int) *scopedOutcome {
	return &scopedOutcome{
		cluster: cl,
		issues:  rc.still,
		gate:    alert.RecoverGate{OK: true, Scope: rc.scope},
		round:   round,
	}
}

func (rc *rechecker) recheck(now time.Time) {
	if rc.clients == nil || rc.clients.Clientset == nil {
		return
	}
	for name, fps := range rc.nodes {
		if rc.ctx.Err() != nil {
			return
		}
		rc.recheckNode(name, fps, now)
	}
	for key := range rc.workloads {
		if rc.ctx.Err() != nil {
			return
		}
		rc.controllerState(key.kind, key.ns, key.name)
	}
	for key, fps := range rc.pods {
		if rc.ctx.Err() != nil {
			return
		}
		rc.recheckPod(key, fps, now)
	}
}

func (rc *rechecker) recheckNode(name string, fps []string, now time.Time) {
	node, err := rc.clients.GetNode(rc.ctx, name)
	if apierrors.IsNotFound(err) {
		rc.addScope(fps...)
		return
	}
	if err != nil {
		log.Printf("recovery probe node %s: %v", name, err)
		return
	}
	issues := kube.NodeConditionIssues(*node, now)
	rc.addScope(fps...)
	rc.addStill(issues, true)
}

func (rc *rechecker) recheckPod(key podKey, fps []string, now time.Time) {
	pod, err := rc.clients.GetPod(rc.ctx, key.ns, key.name)
	if apierrors.IsNotFound(err) {
		rc.recheckMissingPod(key, fps)
		return
	}
	if err != nil {
		log.Printf("recovery probe pod %s/%s: %v", key.ns, key.name, err)
		return
	}
	kind, name, naked := rc.liveController(pod)
	if naked {
		rc.recheckNakedPod(*pod, fps, now)
		return
	}
	if kind == "" {
		return
	}
	if podMayClose(rc.controllerState(kind, pod.Namespace, name)) {
		rc.addScope(fps...)
	}
}

func (rc *rechecker) recheckMissingPod(key podKey, fps []string) {
	ctrls, err := rc.controllersIn(key.ns)
	if err != nil {
		log.Printf("recovery probe controllers %s: %v", key.ns, err)
		return
	}
	match, ok := bestController(key.name, ctrls)
	if !ok {
		return
	}
	res := rc.controllerState(match.kind, match.ns, match.name)
	if podMayClose(res) {
		rc.addScope(fps...)
	}
}

func (rc *rechecker) recheckNakedPod(pod corev1.Pod, fps []string, now time.Time) {
	issues := kube.PodIssues(pod, now)
	if len(issues) == 0 && !podLooksHealthy(pod) {
		return
	}
	rc.addScope(fps...)
	rc.addStill(issues, true)
}

// liveController resolves a running pod to its Deployment, StatefulSet, DaemonSet, or ReplicaSet.
// naked is set for pods with no such owner (including Jobs). The bool kind=="" with naked false
// means the owner could not be resolved, so the pod alert stays open.
func (rc *rechecker) liveController(pod *corev1.Pod) (kind, name string, naked bool) {
	ns := pod.Namespace
	rawKind, rawName := owner.ResolvePodController(pod.OwnerReferences, nil)
	switch rawKind {
	case "", "Pod":
		return "", "", true
	case "Job", "CronJob":
		return "", "", true
	case "StatefulSet", "DaemonSet", "Deployment":
		return rawKind, rawName, false
	case "ReplicaSet":
		rs, err := rc.clients.GetReplicaSet(rc.ctx, ns, rawName)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				log.Printf("recovery probe replicaset %s/%s: %v", ns, rawName, err)
			}
			return "", "", false
		}
		if deploy, ok := deploymentOwner(rs.OwnerReferences); ok {
			return "Deployment", deploy, false
		}
		return "ReplicaSet", rawName, false
	default:
		return "", "", true
	}
}

func deploymentOwner(owners []metav1.OwnerReference) (string, bool) {
	for i := range owners {
		o := &owners[i]
		if o.Kind == "Deployment" && o.Name != "" && o.Controller != nil && *o.Controller {
			return o.Name, true
		}
	}
	return "", false
}

func (rc *rechecker) controllerState(kind, ns, name string) ctrlResult {
	key := ctrlKey{kind: kind, ns: ns, name: name}
	if res, ok := rc.ctrl[key]; ok {
		return res
	}
	res, counts, err := rc.fetchController(kind, ns, name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Printf("recovery probe %s %s/%s: %v", kind, ns, name, err)
			rc.ctrl[key] = ctrlUnknown
			return ctrlUnknown
		}
		res = ctrlGone
	}
	rc.ctrl[key] = res
	rc.noteWorkload(key, res, counts)
	return res
}

type ctrlCounts struct {
	desired, ready, miss int32
	ok                   bool
}

func (rc *rechecker) fetchController(kind, ns, name string) (ctrlResult, ctrlCounts, error) {
	switch kind {
	case "Deployment":
		obj, err := rc.clients.GetDeployment(rc.ctx, ns, name)
		if err != nil {
			return ctrlUnknown, ctrlCounts{}, err
		}
		return resultFromCounts(deploymentCounts(obj)), ctrlCountsFrom(deploymentCounts(obj)), nil
	case "StatefulSet":
		obj, err := rc.clients.GetStatefulSet(rc.ctx, ns, name)
		if err != nil {
			return ctrlUnknown, ctrlCounts{}, err
		}
		d, r := statefulSetCounts(obj)
		return resultFromCounts(d, r, 0), ctrlCountsFrom(d, r, 0), nil
	case "DaemonSet":
		obj, err := rc.clients.GetDaemonSet(rc.ctx, ns, name)
		if err != nil {
			return ctrlUnknown, ctrlCounts{}, err
		}
		d, r, m := daemonSetCounts(obj)
		return resultFromCounts(d, r, m), ctrlCountsFrom(d, r, m), nil
	case "ReplicaSet":
		obj, err := rc.clients.GetReplicaSet(rc.ctx, ns, name)
		if err != nil {
			return ctrlUnknown, ctrlCounts{}, err
		}
		d, r := replicaSetCounts(obj)
		return resultFromCounts(d, r, 0), ctrlCountsFrom(d, r, 0), nil
	default:
		return ctrlUnknown, ctrlCounts{}, nil
	}
}

func ctrlCountsFrom(desired, ready, miss int32) ctrlCounts {
	return ctrlCounts{desired: desired, ready: ready, miss: miss, ok: true}
}

func (rc *rechecker) noteWorkload(key ctrlKey, res ctrlResult, counts ctrlCounts) {
	fps := rc.workloads[key]
	switch res {
	case ctrlGone, ctrlReady:
		rc.addScope(fps...)
	case ctrlNotReady:
		rc.addScope(fps...)
		if counts.ok {
			rc.addStill(workloadStill(key.kind, key.ns, key.name, counts.desired, counts.ready, counts.miss), false)
		}
	}
}

func (rc *rechecker) controllersIn(ns string) ([]namedCtrl, error) {
	if err, ok := rc.nsErr[ns]; ok {
		return nil, err
	}
	if ctrls, ok := rc.nsCtrls[ns]; ok {
		return ctrls, nil
	}
	var out []namedCtrl
	deploys, err := rc.clients.Clientset.AppsV1().Deployments(ns).List(rc.ctx, metav1.ListOptions{})
	if err != nil {
		rc.nsErr[ns] = err
		return nil, err
	}
	for i := range deploys.Items {
		d, r, m := deploymentCounts(&deploys.Items[i])
		out = append(out, namedCtrl{kind: "Deployment", ns: ns, name: deploys.Items[i].Name, desired: d, ready: r, miss: m})
	}
	sets, err := rc.clients.Clientset.AppsV1().StatefulSets(ns).List(rc.ctx, metav1.ListOptions{})
	if err != nil {
		rc.nsErr[ns] = err
		return nil, err
	}
	for i := range sets.Items {
		d, r := statefulSetCounts(&sets.Items[i])
		out = append(out, namedCtrl{kind: "StatefulSet", ns: ns, name: sets.Items[i].Name, desired: d, ready: r})
	}
	daemons, err := rc.clients.Clientset.AppsV1().DaemonSets(ns).List(rc.ctx, metav1.ListOptions{})
	if err != nil {
		rc.nsErr[ns] = err
		return nil, err
	}
	for i := range daemons.Items {
		d, r, m := daemonSetCounts(&daemons.Items[i])
		out = append(out, namedCtrl{kind: "DaemonSet", ns: ns, name: daemons.Items[i].Name, desired: d, ready: r, miss: m})
	}
	rc.nsCtrls[ns] = out
	for _, c := range out {
		key := ctrlKey{kind: c.kind, ns: c.ns, name: c.name}
		if _, ok := rc.ctrl[key]; ok {
			continue
		}
		res := resultFromCounts(c.desired, c.ready, c.miss)
		rc.ctrl[key] = res
		rc.noteWorkload(key, res, ctrlCounts{desired: c.desired, ready: c.ready, miss: c.miss, ok: true})
	}
	return out, nil
}

func (rc *rechecker) addScope(fps ...string) {
	for _, fp := range fps {
		if strings.TrimSpace(fp) == "" {
			continue
		}
		rc.scope[alert.CanonicalFingerprint(fp)] = struct{}{}
	}
}

func (rc *rechecker) addStill(issues []kube.Issue, allowNew bool) {
	for _, it := range issues {
		fp := alert.IssueFingerprint(it)
		if !allowNew {
			if _, ok := rc.sentCanon[fp]; !ok {
				continue
			}
		}
		if _, ok := rc.stillSeen[fp]; ok {
			continue
		}
		rc.stillSeen[fp] = struct{}{}
		rc.still = append(rc.still, it)
	}
}

func podMayClose(res ctrlResult) bool {
	return res == ctrlReady || res == ctrlGone
}

func controllerReady(desired, ready, miss int32) bool {
	if desired <= 0 && miss <= 0 {
		return true
	}
	return ready >= desired && miss <= 0
}

func resultFromCounts(desired, ready, miss int32) ctrlResult {
	if controllerReady(desired, ready, miss) {
		return ctrlReady
	}
	return ctrlNotReady
}

func deploymentCounts(d *appsv1.Deployment) (desired, ready, miss int32) {
	desired = 1
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	return desired, d.Status.ReadyReplicas, 0
}

func statefulSetCounts(s *appsv1.StatefulSet) (desired, ready int32) {
	desired = 1
	if s.Spec.Replicas != nil {
		desired = *s.Spec.Replicas
	}
	return desired, s.Status.ReadyReplicas
}

func daemonSetCounts(ds *appsv1.DaemonSet) (desired, ready, miss int32) {
	return ds.Status.DesiredNumberScheduled, ds.Status.NumberReady, ds.Status.NumberMisscheduled
}

func replicaSetCounts(rs *appsv1.ReplicaSet) (desired, ready int32) {
	desired = 1
	if rs.Spec.Replicas != nil {
		desired = *rs.Spec.Replicas
	}
	return desired, rs.Status.ReadyReplicas
}

func workloadStill(kind, ns, name string, desired, ready, miss int32) []kube.Issue {
	if desired <= 0 && miss <= 0 {
		return nil
	}
	if desired > 0 && ready < desired {
		sev := "error"
		if ready == 0 {
			sev = "critical"
		}
		msg := "ready " + strconv.FormatInt(int64(ready), 10) + "/" + strconv.FormatInt(int64(desired), 10)
		if miss > 0 {
			msg += " · misscheduled " + strconv.FormatInt(int64(miss), 10)
		}
		return []kube.Issue{{
			Severity: sev, Category: "workload", Kind: kind,
			Namespace: ns, Name: name, Reason: "NotReady", Message: msg,
		}}
	}
	if miss > 0 {
		return []kube.Issue{{
			Severity: "warning", Category: "workload", Kind: kind,
			Namespace: ns, Name: name, Reason: "Misscheduled",
			Message: "misscheduled " + strconv.FormatInt(int64(miss), 10),
		}}
	}
	return nil
}

func podLooksHealthy(p corev1.Pod) bool {
	if p.Status.Phase == corev1.PodSucceeded {
		return true
	}
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	if len(p.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

func bestController(podName string, ctrls []namedCtrl) (namedCtrl, bool) {
	var best namedCtrl
	bestLen := 0
	found := false
	for _, c := range ctrls {
		if !podOwnedBy(podName, c) {
			continue
		}
		if !found || len(c.name) > bestLen || (len(c.name) == bestLen && c.kind+c.name < best.kind+best.name) {
			best = c
			bestLen = len(c.name)
			found = true
		}
	}
	return best, found
}

func podOwnedBy(podName string, c namedCtrl) bool {
	if c.name == "" || !strings.HasPrefix(podName, c.name+"-") {
		return false
	}
	rest := strings.TrimPrefix(podName, c.name+"-")
	switch c.kind {
	case "StatefulSet":
		return rest != "" && allDigits(rest)
	case "Deployment":
		parts := strings.Split(rest, "-")
		return len(parts) == 2 && dnsLabel(parts[0]) && dnsLabel(parts[1])
	case "DaemonSet":
		// A missing pod is not guessed onto a DaemonSet. One random suffix is also how
		// Jobs and bare pods are named, so a ready DaemonSet would close the wrong alert.
		// A live pod still resolves through its ownerRef.
		return false
	default:
		return false
	}
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func dnsLabel(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
