package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

const (
	diagnoseListPage     = 500
	diagnoseMaxScan      = 3000
	diagnoseMaxNodes     = 500
	diagnoseMaxIssues    = 200
	diagnoseMaxEvents    = 80
	diagnoseNSWorkers    = 4
	diagnoseEventScan    = 500
	diagnosePendingAge   = 2 * time.Minute
	diagnosePendingOther = 15 * time.Minute
	diagnoseRestartWarn  = int32(3)
	diagnoseRestartMax   = 3
	// 回看窗口：说明里写「近 2h 有/无重启」、独立事件是否成行。不是 Restarts 占行门槛。
	diagnoseRestartRecent = 2 * time.Hour
	// Restarts 占行窗口：上次异常退出超过 30d 不再独立占行（不走降级，直接从列表消失）。
	diagnoseRestartDetail = 30 * 24 * time.Hour
	diagnoseRestartDedup  = 2 * time.Minute
	diagnoseEventFresh    = 2 * time.Hour
	// 防误报等待：滚动缺口、普通 Pending 仍用 15m，拉长到 2h 会把卡住的发布瞒掉。
	diagnoseRolloutGrace   = 15 * time.Minute
	diagnoseUsageWarn      = 0.90
	diagnoseMsgMaxRunes    = 400
	diagnoseEvidenceMax    = 12
	diagnoseEventPerIssue  = 5
	diagnoseLogTail        = 40
	diagnoseLogBytes       = int64(16 * 1024)
	diagnoseLogTimeout     = 4 * time.Second
	diagnoseLogWorkers     = 3
	diagnoseLogMaxPods     = 8
	diagnoseLogExcerptMax  = 8
	diagnoseLogLineMax     = 180
	diagnoseLogContext     = 1
	diagnoseMetricsTimeout = 5 * time.Second
)

var podCriticalReasons = map[string]bool{
	"CrashLoopBackOff":     true,
	"ImagePullBackOff":     true,
	"ErrImagePull":         true,
	"CreateContainerError": true,
	"InvalidImageName":     true,
	"OOMKilled":            true,
}

// Issue is one anomaly found by Diagnose.
type Issue struct {
	Severity     string   `json:"severity"`
	BaseSeverity string   `json:"baseSeverity,omitempty"`
	Category     string   `json:"category"`
	Kind         string   `json:"kind"`
	Namespace    string   `json:"namespace,omitempty"`
	Name         string   `json:"name"`
	Reason       string   `json:"reason"`
	Message      string   `json:"message"`
	Age          string   `json:"age"`
	Since        string   `json:"since,omitempty"`
	SinceAt      string   `json:"sinceAt,omitempty"`
	Node         string   `json:"node,omitempty"`
	NodeIP       string   `json:"nodeIP,omitempty"`
	Container    string   `json:"container,omitempty"`
	Reporter     string   `json:"reporter,omitempty"`
	Count        int32    `json:"count,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	LogPath      string   `json:"logPath,omitempty"`
	LogSource    string   `json:"logSource,omitempty"`
	LogExcerpt   []string `json:"logExcerpt,omitempty"`
}

// DiagnoseCounts is per-category issue totals.
type DiagnoseCounts struct {
	Nodes     int `json:"nodes"`
	Workloads int `json:"workloads"`
	Pods      int `json:"pods"`
	Events    int `json:"events"`
	Storage   int `json:"storage"`
	Critical  int `json:"critical"`
	Error     int `json:"error"`
	Warning   int `json:"warning"`
	Info      int `json:"info"`
}

// DiagnoseScan is what this pass actually listed (for verifying coverage).
type DiagnoseScan struct {
	Nodes          int    `json:"nodes"`
	Pods           int    `json:"pods"`
	Deployments    int    `json:"deployments"`
	StatefulSets   int    `json:"statefulSets"`
	DaemonSets     int    `json:"daemonSets"`
	Events         int    `json:"events"`
	EventsAttached int    `json:"eventsAttached"`
	LogsFetched    int    `json:"logsFetched"`
	ElapsedMs      int64  `json:"elapsedMs"`
	FromCache      bool   `json:"fromCache"`
	MetricsSkipped string `json:"metricsSkipped,omitempty"`
	// LargePods / NFS*: filled by storage scan (kubelet summary + default/unique NFS SC).
	LargePods         int      `json:"largePods"`
	LargePodSkipped   string   `json:"largePodSkipped,omitempty"`
	NFSUsagePercent   *float64 `json:"nfsUsagePercent,omitempty"`
	NFSUsedBytes      int64    `json:"nfsUsedBytes,omitempty"`
	NFSCapacityBytes  int64    `json:"nfsCapacityBytes,omitempty"`
	NFSStorageClass   string   `json:"nfsStorageClass,omitempty"`
	NFSServer         string   `json:"nfsServer,omitempty"` // IP or hostname
	NFSPath           string   `json:"nfsPath,omitempty"`   // export path / share
	NFSSource         string   `json:"nfsSource,omitempty"` // default | unique
	NFSProbe          string   `json:"nfsProbe,omitempty"`  // summary | exec-df
	NFSSkipped        string   `json:"nfsSkipped,omitempty"`
}

// DiagnoseReport is the diagnose API payload: standing problems only.
type DiagnoseReport struct {
	Issues       []Issue           `json:"issues"`
	Counts       DiagnoseCounts    `json:"counts"`
	Scan         DiagnoseScan      `json:"scan"`
	Truncated    bool              `json:"truncated"`
	SkippedNodes bool              `json:"skippedNodes"`
	Errors       map[string]string `json:"errors,omitempty"`
}

// DiagnoseOptions controls a cluster scan.
type DiagnoseOptions struct {
	Namespace   string
	PermType    string
	FallbackNS  []string
	Now         time.Time
	OnCacheMiss func()
}

func shouldScanNodes(permType string) bool {
	return permType != "project"
}

// Diagnose scans nodes, workloads, pods and Warning events and returns only standing problems.
func (c *Clients) Diagnose(ctx context.Context, opt DiagnoseOptions) (*DiagnoseReport, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	started := time.Now()
	rep := &DiagnoseReport{
		Issues:       make([]Issue, 0),
		SkippedNodes: !shouldScanNodes(opt.PermType),
		Errors:       map[string]string{},
		Scan:         DiagnoseScan{FromCache: true},
	}

	var mu sync.Mutex
	addErr := func(cat, msg string) {
		if msg == "" {
			return
		}
		mu.Lock()
		rep.Errors[cat] = msg
		mu.Unlock()
	}
	addIssues := func(items []Issue, trunc bool) {
		mu.Lock()
		defer mu.Unlock()
		if trunc {
			rep.Truncated = true
		}
		if overflow := appendCapped(&rep.Issues, items, diagnoseMaxIssues*4); overflow {
			rep.Truncated = true
		}
	}
	addScan := func(fn func(*DiagnoseScan)) {
		mu.Lock()
		fn(&rep.Scan)
		mu.Unlock()
	}
	opt.OnCacheMiss = func() {
		addScan(func(s *DiagnoseScan) { s.FromCache = false })
	}

	var (
		events       []corev1.Event
		eventsTrunc  bool
		listedPods   []corev1.Pod
		workloadRefs []workloadRef
		locMu        sync.Mutex
		podLocs      = map[string]resourceLoc{}
		podOK        = map[string]bool{}
		nodeIPs      = map[string]string{}
		nodeOutage   bool
	)

	var wg sync.WaitGroup
	run := func(cat string, fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					addErr(cat, fmt.Sprintf("panic: %v", rec))
				}
			}()
			fn()
		}()
	}

	if shouldScanNodes(opt.PermType) {
		run("nodes", func() {
			nodes, nTrunc, err := c.listNodesPaged(ctx, opt, diagnoseMaxNodes)
			if err != nil {
				addErr("nodes", err.Error())
				return
			}
			addScan(func(s *DiagnoseScan) { s.Nodes = len(nodes) })
			outage := clusterNodeOutage(nodes)
			mu.Lock()
			nodeOutage = outage
			mu.Unlock()
			for _, n := range nodes {
				if ip := nodeInternalIP(n); ip != "" {
					locMu.Lock()
					nodeIPs[n.Name] = ip
					locMu.Unlock()
				}
			}
			usage := map[string]nodeUsage{}
			mctx, cancel := context.WithTimeout(ctx, diagnoseMetricsTimeout)
			mets, _, mErr := c.listNodeMetricsPaged(mctx, diagnoseMaxNodes)
			cancel()
			if mErr != nil {
				addScan(func(s *DiagnoseScan) {
					s.MetricsSkipped = clipRunes(mErr.Error(), 240)
				})
			} else {
				for _, m := range mets {
					usage[m.Name] = nodeUsageFromMetrics(m)
				}
			}
			var issues []Issue
			for _, n := range nodes {
				issues = append(issues, diagnoseNode(n, usage[n.Name], opt.Now)...)
			}
			if keepTopIssues(&issues, diagnoseMaxIssues) || nTrunc {
				nTrunc = true
			}
			addIssues(issues, nTrunc)
		})
	}

	run("pods", func() {
		pods, trunc, err := c.listPodsDiagnose(ctx, opt)
		if err != nil {
			addErr("pods", err.Error())
			return
		}
		addScan(func(s *DiagnoseScan) { s.Pods = len(pods) })
		mu.Lock()
		listedPods = pods
		mu.Unlock()
		for _, p := range pods {
			locMu.Lock()
			podLocs[issueKey("Pod", p.Namespace, p.Name)] = resourceLoc{
				Node:   p.Spec.NodeName,
				NodeIP: p.Status.HostIP,
			}
			podOK[issueKey("Pod", p.Namespace, p.Name)] = podCurrentlyOK(p)
			locMu.Unlock()
		}
		var issues []Issue
		for _, p := range pods {
			issues = append(issues, diagnosePod(p, opt.Now)...)
		}
		if keepTopIssues(&issues, diagnoseMaxIssues) {
			trunc = true
		}
		addIssues(issues, trunc)
	})

	run("workloads", func() {
		issues, refs, trunc, err := c.scanWorkloads(ctx, opt, addScan)
		if err != nil {
			addErr("workloads", err.Error())
			return
		}
		mu.Lock()
		workloadRefs = refs
		mu.Unlock()
		if keepTopIssues(&issues, diagnoseMaxIssues) {
			trunc = true
		}
		addIssues(issues, trunc)
	})

	run("events", func() {
		ev, trunc, err := c.listWarningEvents(ctx, opt)
		if err != nil {
			addErr("events", err.Error())
			return
		}
		events = ev
		eventsTrunc = trunc
		addScan(func(s *DiagnoseScan) { s.Events = len(ev) })
	})

	wg.Wait()

	rep.Issues = attachRestartHistory(rep.Issues, listedPods, workloadRefs, events, opt.Now)
	merged, attached := attachEvents(rep.Issues, events, opt.Now, podOK)
	rep.Issues = merged
	enrichLocations(rep.Issues, podLocs, nodeIPs)
	applySeverityDecay(rep.Issues, opt.Now, nodeOutage)
	if eventsTrunc {
		rep.Truncated = true
	}
	sortIssues(rep.Issues)
	if overflow := capCategory(&rep.Issues, diagnoseMaxIssues*4); overflow {
		rep.Truncated = true
	}
	fetched := c.attachCriticalPodLogs(ctx, rep.Issues)
	rep.Scan.LogsFetched = fetched
	rep.Scan.EventsAttached = attached

	// Storage is best-effort after core diagnose + logs, so it only consumes leftover budget.
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				msg := clipRunes(fmt.Sprintf("panic: %v", rec), 240)
				if rep.Scan.LargePodSkipped == "" {
					rep.Scan.LargePodSkipped = msg
				}
				if rep.Scan.NFSSkipped == "" {
					rep.Scan.NFSSkipped = msg
				}
			}
		}()
		storageIssues, storageTrunc := c.scanStorage(ctx, opt, listedPods, &rep.Scan)
		if storageTrunc {
			rep.Truncated = true
		}
		if len(storageIssues) > 0 {
			rep.Issues = append(rep.Issues, storageIssues...)
			// LargeEphemeral arrives after the first enrichLocations pass — fill NodeIP now.
			enrichLocations(rep.Issues, podLocs, nodeIPs)
			sortIssues(rep.Issues)
			if overflow := capCategory(&rep.Issues, diagnoseMaxIssues*4); overflow {
				rep.Truncated = true
			}
		}
	}()

	// Bake location (NFS IP / node / 节点) into Message for alerts + API + UI.
	FinalizeIssueMessages(rep.Issues)

	rep.Scan.ElapsedMs = time.Since(started).Milliseconds()

	rep.Counts = countIssues(rep.Issues)
	if len(rep.Errors) == 0 {
		rep.Errors = nil
	}
	return rep, nil
}

type nodeUsage struct {
	valid bool
	cpu   float64
	mem   float64
}

func nodeUsageFromMetrics(m metricsv1beta1.NodeMetrics) nodeUsage {
	u := nodeUsage{valid: true}
	if q := m.Usage.Cpu(); q != nil {
		u.cpu = q.AsApproximateFloat64()
	}
	if q := m.Usage.Memory(); q != nil {
		u.mem = q.AsApproximateFloat64()
	}
	return u
}

func diagnoseNode(n corev1.Node, usage nodeUsage, now time.Time) []Issue {
	age := formatAgeAt(n.CreationTimestamp.Time, now)
	ip := nodeInternalIP(n)
	base := []string{}
	if ip != "" {
		base = append(base, "internalIP="+ip)
	}
	if usage.valid {
		cpuAlloc := n.Status.Allocatable.Cpu().AsApproximateFloat64()
		memAlloc := n.Status.Allocatable.Memory().AsApproximateFloat64()
		if cpuAlloc > 0 {
			base = append(base, fmt.Sprintf("cpu=%s/%s (%.0f%%)", fmtCores(usage.cpu), n.Status.Allocatable.Cpu().String(), usage.cpu/cpuAlloc*100))
		}
		if memAlloc > 0 {
			base = append(base, fmt.Sprintf("memory=%s/%s (%.0f%%)", fmtBytes(usage.mem), n.Status.Allocatable.Memory().String(), usage.mem/memAlloc*100))
		}
	}

	var out []Issue
	ready := "Unknown"
	var readyMsg string
	var readySince time.Time
	var readyCond *corev1.NodeCondition
	for i := range n.Status.Conditions {
		cond := n.Status.Conditions[i]
		if cond.Type != corev1.NodeReady {
			continue
		}
		readyCond = &n.Status.Conditions[i]
		readySince = cond.LastTransitionTime.Time
		switch cond.Status {
		case corev1.ConditionTrue:
			ready = "Ready"
		case corev1.ConditionFalse:
			ready = "NotReady"
			readyMsg = strings.TrimSpace(cond.Reason + " " + cond.Message)
		default:
			ready = "Unknown"
			readyMsg = strings.TrimSpace(cond.Reason + " " + cond.Message)
		}
	}
	if ready != "Ready" {
		msg := ready
		if readyMsg != "" {
			msg = clipRunes(readyMsg, diagnoseMsgMaxRunes)
		}
		ev := append([]string{}, base...)
		if readyCond != nil {
			ev = append(ev, fmt.Sprintf("Ready=%s reason=%s", readyCond.Status, readyCond.Reason))
			if !readyCond.LastHeartbeatTime.IsZero() {
				ev = append(ev, "lastHeartbeat="+formatAgeAt(readyCond.LastHeartbeatTime.Time, now)+" ago")
			}
			if readyCond.Message != "" {
				ev = append(ev, "message="+clipRunes(readyCond.Message, 240))
			}
		} else {
			ev = append(ev, "Ready condition missing")
		}
		it := Issue{
			Severity: "critical", Category: "node", Kind: "Node",
			Name: n.Name, NodeIP: ip, Reason: ready, Message: msg, Age: age,
			Evidence: clipEvidence(ev),
		}
		stampIssueSince(&it, readySince, now)
		out = append(out, it)
	}

	pressures := []corev1.NodeConditionType{
		corev1.NodeDiskPressure,
		corev1.NodeMemoryPressure,
		corev1.NodePIDPressure,
		corev1.NodeNetworkUnavailable,
	}
	for _, t := range pressures {
		for _, cond := range n.Status.Conditions {
			if cond.Type != t || cond.Status != corev1.ConditionTrue {
				continue
			}
			reason := string(t)
			msg := strings.TrimSpace(cond.Reason + " " + cond.Message)
			if msg == "" {
				msg = reason
			}
			ev := append([]string{}, base...)
			ev = append(ev, fmt.Sprintf("%s=True reason=%s", t, cond.Reason))
			if cond.Message != "" {
				ev = append(ev, "message="+clipRunes(cond.Message, 240))
			}
			it := Issue{
				Severity: "error", Category: "node", Kind: "Node",
				Name: n.Name, NodeIP: ip, Reason: reason, Message: clipRunes(msg, diagnoseMsgMaxRunes),
				Age: age, Evidence: clipEvidence(ev),
			}
			stampIssueSince(&it, cond.LastTransitionTime.Time, now)
			out = append(out, it)
		}
	}

	if usage.valid {
		cpuAlloc := n.Status.Allocatable.Cpu().AsApproximateFloat64()
		memAlloc := n.Status.Allocatable.Memory().AsApproximateFloat64()
		if cpuAlloc > 0 && usage.cpu/cpuAlloc >= diagnoseUsageWarn {
			pct := usage.cpu / cpuAlloc * 100
			out = append(out, Issue{
				Severity: "warning", Category: "node", Kind: "Node",
				Name: n.Name, NodeIP: ip, Reason: "CPUHigh",
				Message:  fmt.Sprintf("CPU %s / %s（%.0f%%，阈值 %.0f%%）", fmtCores(usage.cpu), n.Status.Allocatable.Cpu().String(), pct, diagnoseUsageWarn*100),
				Age:      age,
				Evidence: clipEvidence(append(append([]string{}, base...), fmt.Sprintf("threshold=%.0f%%", diagnoseUsageWarn*100))),
			})
		}
		if memAlloc > 0 && usage.mem/memAlloc >= diagnoseUsageWarn {
			pct := usage.mem / memAlloc * 100
			out = append(out, Issue{
				Severity: "warning", Category: "node", Kind: "Node",
				Name: n.Name, NodeIP: ip, Reason: "MemoryHigh",
				Message:  fmt.Sprintf("内存 %s / %s（%.0f%%，阈值 %.0f%%）", fmtBytes(usage.mem), n.Status.Allocatable.Memory().String(), pct, diagnoseUsageWarn*100),
				Age:      age,
				Evidence: clipEvidence(append(append([]string{}, base...), fmt.Sprintf("threshold=%.0f%%", diagnoseUsageWarn*100))),
			})
		}
	}
	return out
}

func diagnosePod(p corev1.Pod, now time.Time) []Issue {
	if p.Status.Phase == corev1.PodSucceeded {
		return nil
	}
	age := formatAgeAt(p.CreationTimestamp.Time, now)
	facts := podFacts(p, now)
	issue := func(sev, reason, msg string, since time.Time) []Issue {
		it := Issue{
			Severity: sev, Category: "pod", Kind: "Pod",
			Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName, NodeIP: p.Status.HostIP,
			Reason: reason, Message: clipRunes(msg, diagnoseMsgMaxRunes),
			Age: age, Evidence: clipEvidence(facts),
		}
		stampIssueSince(&it, since, now)
		applyPodLogHint(&it, p)
		return []Issue{it}
	}

	if reason, msg, critical := podContainerProblem(p); critical {
		return issue("critical", reason, msg, podUnhealthySince(p))
	}

	if p.Status.Phase == corev1.PodFailed {
		reason := p.Status.Reason
		if reason == "" {
			reason = "Failed"
		}
		if ignoreSettledPod(reason) {
			return nil
		}
		msg := p.Status.Message
		if msg == "" {
			msg = "Pod phase is Failed"
		}
		return issue("critical", reason, msg, podUnhealthySince(p))
	}

	if p.Status.Phase == corev1.PodUnknown {
		msg := p.Status.Message
		if msg == "" {
			msg = "Pod phase is Unknown"
		}
		return issue("error", "Unknown", msg, podUnhealthySince(p))
	}

	if p.Status.Phase == corev1.PodRunning && !podContainersReady(p) {
		ready, total := podReadyCounts(p)
		return issue("error", "NotReady", fmt.Sprintf("ready %d/%d", ready, total), podUnhealthySince(p))
	}

	if p.Status.Phase == corev1.PodPending {
		reason, msg := pendingHint(p)
		minAge := diagnosePendingOther
		if reason == "Unschedulable" {
			minAge = diagnosePendingAge
		}
		if now.Sub(p.CreationTimestamp.Time) < minAge {
			return nil
		}
		since := conditionFalseTime(p, corev1.PodScheduled)
		if since.IsZero() {
			since = conditionTime(p, corev1.PodScheduled)
		}
		if since.IsZero() {
			since = p.CreationTimestamp.Time
		}
		if reason == "Unschedulable" {
			return issue("error", reason, msg, since)
		}
		return issue("warning", reason, msg, since)
	}

	restarts := podAlertRestartCount(p)
	if restarts >= diagnoseRestartWarn {
		last := lastRestartAt(p)
		// restartCount 是生命周期累计；Completed/exit=0 已剔除；超过 30d 的旧异常退出不再占行。
		if !last.IsZero() && now.Sub(last) > diagnoseRestartDetail {
			return nil
		}
		return issue("warning", "Restarts", formatPodRestartMessage(p, restarts, last, now), last)
	}
	return nil
}

func diagnoseDeployment(d appsv1.Deployment, now time.Time) []Issue {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	ev := []string{
		fmt.Sprintf("replicas desired=%d ready=%d updated=%d available=%d unavailable=%d",
			desired, d.Status.ReadyReplicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas, d.Status.UnavailableReplicas),
	}
	since, extra := workloadFalseConditions(d.Status.Conditions)
	ev = append(ev, extra...)
	if desired > 0 && d.Status.ReadyReplicas < desired && deploymentRolloutRecent(d, now) {
		return nil
	}
	return replicaIssues("Deployment", d.Namespace, d.Name, desired, d.Status.ReadyReplicas, 0, d.CreationTimestamp.Time, since, now, ev)
}

func diagnoseStatefulSet(s appsv1.StatefulSet, now time.Time) []Issue {
	desired := int32(1)
	if s.Spec.Replicas != nil {
		desired = *s.Spec.Replicas
	}
	ev := []string{
		fmt.Sprintf("replicas desired=%d ready=%d current=%d updated=%d",
			desired, s.Status.ReadyReplicas, s.Status.CurrentReplicas, s.Status.UpdatedReplicas),
	}
	since, extra := statefulSetFalseConditions(s.Status.Conditions)
	ev = append(ev, extra...)
	if desired > 0 && s.Status.ReadyReplicas < desired && statefulSetRolloutRecent(s, now) {
		return nil
	}
	return replicaIssues("StatefulSet", s.Namespace, s.Name, desired, s.Status.ReadyReplicas, 0, s.CreationTimestamp.Time, since, now, ev)
}

func diagnoseDaemonSet(ds appsv1.DaemonSet, now time.Time) []Issue {
	desired := ds.Status.DesiredNumberScheduled
	ready := ds.Status.NumberReady
	miss := ds.Status.NumberMisscheduled
	ev := []string{
		fmt.Sprintf("desired=%d ready=%d available=%d misscheduled=%d updated=%d",
			desired, ready, ds.Status.NumberAvailable, miss, ds.Status.UpdatedNumberScheduled),
	}
	since, extra := daemonSetFalseConditions(ds.Status.Conditions)
	ev = append(ev, extra...)
	if desired > 0 && ready < desired && daemonSetRolloutRecent(ds, now) {
		if miss > 0 {
			return replicaIssues("DaemonSet", ds.Namespace, ds.Name, desired, desired, miss, ds.CreationTimestamp.Time, since, now, ev)
		}
		return nil
	}
	return replicaIssues("DaemonSet", ds.Namespace, ds.Name, desired, ready, miss, ds.CreationTimestamp.Time, since, now, ev)
}

func replicaIssues(kind, ns, name string, desired, ready, misscheduled int32, created, since, now time.Time, evidence []string) []Issue {
	if desired <= 0 && misscheduled <= 0 {
		return nil
	}
	age := formatAgeAt(created, now)
	sinceAge := formatAgeAt(since, now)
	if desired > 0 && ready < desired {
		sev := "error"
		if ready == 0 {
			sev = "critical"
		}
		msg := fmt.Sprintf("ready %d/%d", ready, desired)
		if misscheduled > 0 {
			msg += fmt.Sprintf(" · misscheduled %d", misscheduled)
		}
		it := Issue{
			Severity: sev, Category: "workload", Kind: kind,
			Namespace: ns, Name: name, Reason: "NotReady", Message: msg,
			Age: age, Evidence: clipEvidence(evidence),
		}
		stampIssueSince(&it, since, now)
		if it.Since == "" {
			it.Since = sinceAge
		}
		return []Issue{it}
	}
	if misscheduled > 0 {
		it := Issue{
			Severity: "warning", Category: "workload", Kind: kind,
			Namespace: ns, Name: name, Reason: "Misscheduled",
			Message: fmt.Sprintf("misscheduled %d", misscheduled),
			Age:     age, Evidence: clipEvidence(evidence),
		}
		stampIssueSince(&it, since, now)
		if it.Since == "" {
			it.Since = sinceAge
		}
		return []Issue{it}
	}
	return nil
}

func deploymentRolloutRecent(d appsv1.Deployment, now time.Time) bool {
	var p *appsv1.DeploymentCondition
	for i := range d.Status.Conditions {
		if d.Status.Conditions[i].Type == appsv1.DeploymentProgressing {
			p = &d.Status.Conditions[i]
			break
		}
	}
	if p == nil || p.Status != corev1.ConditionTrue {
		return false
	}
	t := p.LastUpdateTime.Time
	if t.IsZero() {
		t = p.LastTransitionTime.Time
	}
	if t.IsZero() || now.Sub(t) > diagnoseRolloutGrace {
		return false
	}
	switch p.Reason {
	case "ReplicaSetUpdated", "NewReplicaSetCreated", "FoundNewReplicaSet", "NewReplicaSetAvailable":
		return true
	}
	return d.Status.UnavailableReplicas > 0
}

func statefulSetRolloutRecent(s appsv1.StatefulSet, now time.Time) bool {
	if s.Status.UpdateRevision == "" || s.Status.CurrentRevision == "" || s.Status.UpdateRevision == s.Status.CurrentRevision {
		return false
	}
	for _, c := range s.Status.Conditions {
		if string(c.Type) != "Progressing" || c.Status != corev1.ConditionTrue {
			continue
		}
		t := c.LastTransitionTime.Time
		return !t.IsZero() && now.Sub(t) <= diagnoseRolloutGrace
	}
	return false
}

func daemonSetRolloutRecent(ds appsv1.DaemonSet, now time.Time) bool {
	if ds.Status.DesiredNumberScheduled <= 0 {
		return false
	}
	if ds.Status.UpdatedNumberScheduled >= ds.Status.DesiredNumberScheduled {
		return false
	}
	for _, c := range ds.Status.Conditions {
		if string(c.Type) != "Progressing" || c.Status != corev1.ConditionTrue {
			continue
		}
		t := c.LastTransitionTime.Time
		return !t.IsZero() && now.Sub(t) <= diagnoseRolloutGrace
	}
	return false
}

func diagnoseEvent(e corev1.Event, now time.Time) Issue {
	when := eventTime(e)
	ns := firstNonEmpty(e.InvolvedObject.Namespace, e.Namespace)
	kind := e.InvolvedObject.Kind
	if kind == "" {
		kind = "Event"
	}
	reason := e.Reason
	if reason == "" {
		reason = "Warning"
	}
	node := firstNonEmpty(strings.TrimSpace(e.Source.Host), strings.TrimSpace(e.ReportingInstance))
	cname := containerFromFieldPath(e.InvolvedObject.FieldPath)
	reporter := firstNonEmpty(e.ReportingController, e.Source.Component)
	msg := e.Message
	if e.Count > 1 {
		msg = fmt.Sprintf("x%d · %s", e.Count, e.Message)
	}
	ev := []string{eventEvidenceLine(e, now)}
	it := Issue{
		Severity:  "warning",
		Category:  "event",
		Kind:      kind,
		Namespace: ns,
		Name:      e.InvolvedObject.Name,
		Reason:    reason,
		Message:   clipRunes(msg, diagnoseMsgMaxRunes),
		Age:       formatAgeAt(when, now),
		Node:      node,
		Container: cname,
		Reporter:  reporter,
		Count:     e.Count,
		Evidence:  clipEvidence(ev),
	}
	stampIssueSince(&it, when, now)
	return it
}

type resourceLoc struct {
	Node, NodeIP string
}

func enrichLocations(issues []Issue, podLocs map[string]resourceLoc, nodeIPs map[string]string) {
	for i := range issues {
		it := &issues[i]
		if loc, ok := podLocs[issueKey(it.Kind, it.Namespace, it.Name)]; ok {
			if it.Node == "" {
				it.Node = loc.Node
			}
			if it.NodeIP == "" {
				it.NodeIP = loc.NodeIP
			}
		}
		if it.NodeIP == "" && it.Node != "" && nodeIPs != nil {
			it.NodeIP = nodeIPs[it.Node]
		}
	}
}

func containerFromFieldPath(fp string) string {
	fp = strings.TrimSpace(fp)
	i := strings.Index(fp, "{")
	j := strings.LastIndex(fp, "}")
	if i >= 0 && j > i {
		return strings.TrimSpace(fp[i+1 : j])
	}
	return ""
}

func attachEvents(issues []Issue, events []corev1.Event, now time.Time, podOK map[string]bool) ([]Issue, int) {
	idx := map[string][]int{}
	for i, it := range issues {
		if it.Category == "event" {
			continue
		}
		idx[issueKey(it.Kind, it.Namespace, it.Name)] = append(idx[issueKey(it.Kind, it.Namespace, it.Name)], i)
	}
	attached := 0
	var orphans []corev1.Event
	for _, e := range events {
		ns := firstNonEmpty(e.InvolvedObject.Namespace, e.Namespace)
		keys := []string{issueKey(e.InvolvedObject.Kind, ns, e.InvolvedObject.Name)}
		if strings.EqualFold(e.InvolvedObject.Kind, "Node") {
			keys = append(keys, issueKey("Node", "", e.InvolvedObject.Name))
		}
		var ids []int
		seen := map[int]bool{}
		for _, k := range keys {
			for _, i := range idx[k] {
				if seen[i] {
					continue
				}
				seen[i] = true
				ids = append(ids, i)
			}
		}
		if len(ids) == 0 {
			if !allowOrphanEvent(e, now, podOK) {
				continue
			}
			orphans = append(orphans, e)
			continue
		}
		if skipAttachEvent(e, now, podOK) {
			continue
		}
		line := eventEvidenceLine(e, now)
		for _, i := range ids {
			if (e.Reason == "BackOff" || e.Reason == "Killing") && hasRestartTimelineEvidence(issues[i].Evidence) {
				continue
			}
			if warningEvidenceCount(issues[i].Evidence) >= diagnoseEventPerIssue {
				continue
			}
			issues[i].Evidence = appendWarningEvidence(issues[i].Evidence, line)
		}
		attached++
	}
	out := issues
	for _, e := range orphans {
		out = append(out, diagnoseEvent(e, now))
	}
	return out, attached
}

func podCurrentlyOK(p corev1.Pod) bool {
	return p.Status.Phase == corev1.PodRunning && podContainersReady(p)
}

func allowOrphanEvent(e corev1.Event, now time.Time, podOK map[string]bool) bool {
	switch e.Reason {
	case "Unhealthy", "ProbeWarning":
		return false
	}
	when := eventTime(e)
	if when.IsZero() || now.Sub(when) > diagnoseEventFresh {
		return false
	}
	ns := firstNonEmpty(e.InvolvedObject.Namespace, e.Namespace)
	kind := e.InvolvedObject.Kind
	key := issueKey(kind, ns, e.InvolvedObject.Name)
	podSeenOK := false
	if strings.EqualFold(kind, "Pod") {
		if ok, seen := podOK[key]; seen && ok {
			podSeenOK = true
		}
		if podSeenOK {
			return false
		}
	}
	switch e.Reason {
	case "FailedScheduling", "FailedMount", "FailedAttachVolume", "FailedCreate", "FailedBinding":
		return true
	case "BackOff", "Killing":
		if e.Reason == "Killing" && !strings.Contains(strings.ToLower(e.Message), "oom") {
			return false
		}
		return true
	case "Evicted":
		if !strings.EqualFold(kind, "Pod") {
			return true
		}
		ok, seen := podOK[key]
		return seen && !ok
	default:
		return false
	}
}

func skipAttachEvent(e corev1.Event, now time.Time, podOK map[string]bool) bool {
	switch e.Reason {
	case "Unhealthy", "ProbeWarning":
	default:
		return false
	}
	ns := firstNonEmpty(e.InvolvedObject.Namespace, e.Namespace)
	if strings.EqualFold(e.InvolvedObject.Kind, "Pod") {
		if ok, seen := podOK[issueKey("Pod", ns, e.InvolvedObject.Name)]; seen && ok {
			return true
		}
	}
	when := eventTime(e)
	return when.IsZero() || now.Sub(when) > diagnoseEventFresh
}

func ignoreSettledPod(reason string) bool {
	switch reason {
	case "Evicted", "Shutdown", "NodeShutdown", "Preempting", "Terminated":
		return true
	default:
		return false
	}
}

func skipPodLogFiles(reason string) bool {
	if ignoreSettledPod(reason) {
		return true
	}
	switch reason {
	case "Unschedulable", "Pending", "Unknown":
		return true
	default:
		return false
	}
}

func podGoneNoLogs(reason string) bool {
	if skipPodLogFiles(reason) {
		return true
	}
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
		return true
	default:
		return false
	}
}

func applyPodLogHint(it *Issue, p corev1.Pod) {
	cname, restarts := problemContainer(p)
	if cname == "" {
		return
	}
	it.Container = cname
	if skipPodLogFiles(it.Reason) {
		return
	}
	prev := wantPreviousLogs(it.Reason, restarts)
	file := restarts
	src := "current"
	if prev {
		if restarts > 0 {
			file = restarts - 1
		}
		src = "previous"
	}
	it.LogSource = fmt.Sprintf("%s container=%s tail=%d", src, cname, diagnoseLogTail)
	if p.UID != "" {
		it.LogPath = fmt.Sprintf("/var/log/pods/%s_%s_%s/%s/%d.log", p.Namespace, p.Name, p.UID, cname, file)
	} else {
		it.LogPath = fmt.Sprintf("/var/log/pods/%s_%s/<uid>/%s/%d.log", p.Namespace, p.Name, cname, file)
	}
}

func wantPreviousLogs(reason string, restarts int32) bool {
	if restarts <= 0 {
		return false
	}
	switch reason {
	case "CrashLoopBackOff", "OOMKilled", "CreateContainerError", "Failed", "Restarts":
		return true
	default:
		return false
	}
}

func problemContainer(p corev1.Pod) (name string, restarts int32) {
	pick := func(cs corev1.ContainerStatus) bool {
		if w := cs.State.Waiting; w != nil && podCriticalReasons[w.Reason] {
			return true
		}
		if t := cs.State.Terminated; t != nil && podCriticalReasons[t.Reason] {
			return true
		}
		if !cs.Ready {
			if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
				return true
			}
		}
		return false
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if pick(cs) {
			return cs.Name, cs.RestartCount
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if pick(cs) {
			return cs.Name, cs.RestartCount
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready || cs.RestartCount >= diagnoseRestartWarn {
			return cs.Name, cs.RestartCount
		}
	}
	if len(p.Status.ContainerStatuses) > 0 {
		cs := p.Status.ContainerStatuses[0]
		return cs.Name, cs.RestartCount
	}
	if len(p.Spec.Containers) > 0 {
		return p.Spec.Containers[0].Name, 0
	}
	return "", 0
}

func shouldFetchPodLogs(it Issue) bool {
	base := it.BaseSeverity
	if base == "" {
		base = it.Severity
	}
	if it.Category != "pod" || base != "critical" {
		return false
	}
	if it.Namespace == "" || it.Name == "" {
		return false
	}
	if podGoneNoLogs(it.Reason) {
		return false
	}
	return true
}

func logFetchPriority(reason string) int {
	switch reason {
	case "CrashLoopBackOff", "OOMKilled":
		return 0
	case "Failed", "CreateContainerError":
		return 1
	default:
		return 2
	}
}

func pickLogIssueIndexes(issues []Issue) []int {
	type cand struct{ i, pri int }
	var cs []cand
	for i, it := range issues {
		if !shouldFetchPodLogs(it) {
			continue
		}
		cs = append(cs, cand{i: i, pri: logFetchPriority(it.Reason)})
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].pri < cs[b].pri })
	if len(cs) > diagnoseLogMaxPods {
		cs = cs[:diagnoseLogMaxPods]
	}
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = c.i
	}
	return out
}

func (c *Clients) attachCriticalPodLogs(ctx context.Context, issues []Issue) int {
	idxs := pickLogIssueIndexes(issues)
	if len(idxs) == 0 {
		return 0
	}
	sem := make(chan struct{}, diagnoseLogWorkers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	fetched := 0
	for _, i := range idxs {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			it := issues[i]
			wantPrev := strings.HasPrefix(it.LogSource, "previous")
			raw, src, err := c.fetchDiagnoseLog(ctx, it, wantPrev)
			mu.Lock()
			defer mu.Unlock()
			if src != "" {
				issues[i].LogSource = src
			}
			if err != nil {
				return
			}
			excerpt := excerptLogs(raw)
			if len(excerpt) == 0 {
				excerpt = []string{"(empty log tail)"}
			}
			issues[i].LogExcerpt = excerpt
			fetched++
		}()
	}
	wg.Wait()
	return fetched
}

func (c *Clients) fetchDiagnoseLog(ctx context.Context, it Issue, previous bool) (raw, source string, err error) {
	source = it.LogSource
	q := PodLogQuery{
		Container:  it.Container,
		Tail:       diagnoseLogTail,
		Previous:   previous,
		LimitBytes: diagnoseLogBytes,
	}
	lctx, cancel := context.WithTimeout(ctx, diagnoseLogTimeout)
	raw, err = c.PodLogsQuery(lctx, it.Namespace, it.Name, q)
	cancel()
	if err == nil {
		return raw, source, nil
	}
	if !previous {
		return "", source, err
	}
	q.Previous = false
	source = strings.Replace(source, "previous", "current", 1)
	lctx, cancel = context.WithTimeout(ctx, diagnoseLogTimeout)
	raw, err = c.PodLogsQuery(lctx, it.Namespace, it.Name, q)
	cancel()
	return raw, source, err
}

func excerptLogs(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	all := strings.Split(raw, "\n")
	lines := make([]string, 0, len(all))
	for _, ln := range all {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			continue
		}
		lines = append(lines, ln)
	}
	if len(lines) == 0 {
		return nil
	}
	keyIdx := make([]int, 0)
	for i, ln := range lines {
		if isKeyLogLine(ln) {
			keyIdx = append(keyIdx, i)
		}
	}
	keep := map[int]bool{}
	if len(keyIdx) == 0 {
		start := len(lines) - diagnoseLogExcerptMax
		if start < 0 {
			start = 0
		}
		for i := start; i < len(lines); i++ {
			keep[i] = true
		}
	} else {
		if len(keyIdx) > diagnoseLogExcerptMax {
			keyIdx = keyIdx[len(keyIdx)-diagnoseLogExcerptMax:]
		}
		for _, i := range keyIdx {
			from := i - diagnoseLogContext
			if from < 0 {
				from = 0
			}
			to := i + diagnoseLogContext
			if to >= len(lines) {
				to = len(lines) - 1
			}
			for j := from; j <= to; j++ {
				keep[j] = true
			}
		}
	}
	out := make([]string, 0, diagnoseLogExcerptMax)
	first := true
	prev := -2
	for i := range lines {
		if !keep[i] {
			continue
		}
		if !first && i > prev+1 {
			out = append(out, "…")
		}
		out = append(out, clipRunes(lines[i], diagnoseLogLineMax))
		prev = i
		first = false
		if len(out) >= diagnoseLogExcerptMax {
			break
		}
	}
	return out
}

func isKeyLogLine(s string) bool {
	l := strings.ToLower(s)
	keys := []string{"error", "fatal", "panic", "oom", "killed", "exception", "traceback", "fail", "crash", "emerg", "crit"}
	for _, k := range keys {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

func eventEvidenceLine(e corev1.Event, now time.Time) string {
	when := formatAgeAt(eventTime(e), now)
	msg := clipRunes(e.Message, 240)
	if e.Count > 1 {
		return fmt.Sprintf("Warning %s ×%d %s: %s", e.Reason, e.Count, when, msg)
	}
	return fmt.Sprintf("Warning %s %s: %s", e.Reason, when, msg)
}

func warningEvidenceCount(ev []string) int {
	n := 0
	for _, s := range ev {
		if strings.HasPrefix(s, "Warning ") {
			n++
		}
	}
	return n
}

func issueKey(kind, ns, name string) string {
	return strings.ToLower(kind) + "|" + ns + "|" + name
}

func podContainerProblem(p corev1.Pod) (reason, msg string, critical bool) {
	groups := [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses}
	for _, group := range groups {
		for _, cs := range group {
			if w := cs.State.Waiting; w != nil && w.Reason != "" && podCriticalReasons[w.Reason] {
				m := w.Message
				if m == "" {
					m = w.Reason
				}
				return w.Reason, m, true
			}
			if t := cs.State.Terminated; t != nil && podCriticalReasons[t.Reason] {
				m := t.Message
				if m == "" {
					m = fmt.Sprintf("%s exit=%d", t.Reason, t.ExitCode)
				}
				return t.Reason, m, true
			}
			if !cs.Ready {
				if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
					m := t.Message
					if m == "" {
						m = "OOMKilled"
					}
					return "OOMKilled", m, true
				}
			}
		}
	}
	return "", "", false
}

func podFacts(p corev1.Pod, now time.Time) []string {
	ready, total := podReadyCounts(p)
	var restarts int32
	for _, cs := range p.Status.ContainerStatuses {
		restarts += cs.RestartCount
	}
	ev := []string{
		fmt.Sprintf("phase=%s ready=%d/%d restarts=%d", p.Status.Phase, ready, total, restarts),
	}
	if p.Spec.NodeName != "" || p.Status.HostIP != "" {
		line := "node=" + p.Spec.NodeName
		if p.Status.HostIP != "" {
			if p.Spec.NodeName != "" {
				line += " "
			}
			line += "ip=" + p.Status.HostIP
		}
		ev = append(ev, line)
	}
	if p.Status.PodIP != "" {
		ev = append(ev, "podIP="+p.Status.PodIP)
	}
	if p.Status.Reason != "" {
		ev = append(ev, "status.reason="+p.Status.Reason)
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if line := containerFact(cs, true, now); line != "" {
			ev = append(ev, line)
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if line := containerFact(cs, false, now); line != "" {
			ev = append(ev, line)
		}
	}
	for _, cond := range p.Status.Conditions {
		if cond.Status == corev1.ConditionTrue {
			continue
		}
		line := fmt.Sprintf("%s=%s %s", cond.Type, cond.Status, cond.Reason)
		if cond.Message != "" {
			line += ": " + clipRunes(cond.Message, 200)
		}
		if !cond.LastTransitionTime.IsZero() {
			line += " since " + formatAgeAt(cond.LastTransitionTime.Time, now)
		}
		ev = append(ev, line)
	}
	return ev
}

func containerFact(cs corev1.ContainerStatus, init bool, now time.Time) string {
	name := cs.Name
	if init {
		name = "init:" + cs.Name
	}
	if w := cs.State.Waiting; w != nil && w.Reason != "" {
		line := fmt.Sprintf("%s waiting %s restarts=%d", name, w.Reason, cs.RestartCount)
		if w.Message != "" {
			line += ": " + clipRunes(w.Message, 160)
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			line += fmt.Sprintf(" | lastExit %s exit=%d", t.Reason, t.ExitCode)
			if !t.FinishedAt.IsZero() {
				line += " " + formatAgeAt(t.FinishedAt.Time, now) + " ago"
			}
		}
		return line
	}
	if t := cs.State.Terminated; t != nil {
		line := fmt.Sprintf("%s terminated %s exit=%d restarts=%d", name, t.Reason, t.ExitCode, cs.RestartCount)
		if t.Message != "" {
			line += ": " + clipRunes(t.Message, 160)
		}
		return line
	}
	if t := cs.LastTerminationState.Terminated; t != nil && cs.RestartCount > 0 {
		return fmt.Sprintf("%s running lastExit %s exit=%d restarts=%d", name, t.Reason, t.ExitCode, cs.RestartCount)
	}
	if !cs.Ready {
		return fmt.Sprintf("%s ready=false restarts=%d", name, cs.RestartCount)
	}
	if cs.RestartCount >= diagnoseRestartWarn {
		return fmt.Sprintf("%s ready=true restarts=%d", name, cs.RestartCount)
	}
	return ""
}

func lastRestartAt(p corev1.Pod) time.Time {
	var latest time.Time
	consider := func(t *corev1.ContainerStateTerminated) {
		if t == nil || t.FinishedAt.IsZero() || terminationBenign(t) {
			return
		}
		if t.FinishedAt.After(latest) {
			latest = t.FinishedAt.Time
		}
	}
	for _, group := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range group {
			consider(cs.State.Terminated)
			consider(cs.LastTerminationState.Terminated)
		}
	}
	if !latest.IsZero() {
		return latest
	}
	for _, group := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range group {
			if containerAlertRestartCount(cs) <= 0 {
				continue
			}
			if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() && r.StartedAt.After(latest) {
				latest = r.StartedAt.Time
			}
		}
	}
	return latest
}

func podAlertRestartCount(p corev1.Pod) int32 {
	var n int32
	for _, cs := range p.Status.ContainerStatuses {
		n += containerAlertRestartCount(cs)
	}
	return n
}

func containerAlertRestartCount(cs corev1.ContainerStatus) int32 {
	if t := latestContainerTermination(cs); terminationBenign(t) {
		return 0
	}
	return cs.RestartCount
}

func latestContainerTermination(cs corev1.ContainerStatus) *corev1.ContainerStateTerminated {
	cur := cs.State.Terminated
	last := cs.LastTerminationState.Terminated
	switch {
	case cur == nil:
		return last
	case last == nil:
		return cur
	case last.FinishedAt.After(cur.FinishedAt.Time):
		return last
	default:
		return cur
	}
}

func terminationBenign(t *corev1.ContainerStateTerminated) bool {
	if t == nil {
		return false
	}
	if t.ExitCode == 0 {
		return true
	}
	switch t.Reason {
	case "Completed", "Success":
		return true
	default:
		return false
	}
}

func conditionTime(p corev1.Pod, t corev1.PodConditionType) time.Time {
	for _, c := range p.Status.Conditions {
		if c.Type == t {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

func conditionFalseTime(p corev1.Pod, t corev1.PodConditionType) time.Time {
	for _, c := range p.Status.Conditions {
		if c.Type == t && c.Status != corev1.ConditionTrue {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

func podUnhealthySince(p corev1.Pod) time.Time {
	for _, t := range []corev1.PodConditionType{corev1.PodReady, corev1.ContainersReady, corev1.PodScheduled} {
		if since := conditionFalseTime(p, t); !since.IsZero() {
			return since
		}
	}
	if p.Status.StartTime != nil && !p.Status.StartTime.IsZero() {
		return p.Status.StartTime.Time
	}
	return p.CreationTimestamp.Time
}

type condView struct {
	Type, Reason, Message string
	Status                corev1.ConditionStatus
	LastTransition        time.Time
}

func falseConditionsSince(views []condView) (time.Time, []string) {
	var earliest, available time.Time
	var extra []string
	for _, c := range views {
		if c.Status == corev1.ConditionTrue {
			continue
		}
		line := fmt.Sprintf("%s=%s %s", c.Type, c.Status, c.Reason)
		if c.Message != "" {
			line += ": " + clipRunes(c.Message, 200)
		}
		extra = append(extra, line)
		if c.LastTransition.IsZero() {
			continue
		}
		if strings.EqualFold(c.Type, "Available") {
			available = c.LastTransition
		}
		if earliest.IsZero() || c.LastTransition.Before(earliest) {
			earliest = c.LastTransition
		}
	}
	if !available.IsZero() {
		return available, extra
	}
	return earliest, extra
}

func workloadFalseConditions(conds []appsv1.DeploymentCondition) (time.Time, []string) {
	views := make([]condView, 0, len(conds))
	for _, c := range conds {
		views = append(views, condView{
			Type: string(c.Type), Status: c.Status, Reason: c.Reason, Message: c.Message,
			LastTransition: c.LastTransitionTime.Time,
		})
	}
	return falseConditionsSince(views)
}

func statefulSetFalseConditions(conds []appsv1.StatefulSetCondition) (time.Time, []string) {
	views := make([]condView, 0, len(conds))
	for _, c := range conds {
		views = append(views, condView{
			Type: string(c.Type), Status: c.Status, Reason: c.Reason, Message: c.Message,
			LastTransition: c.LastTransitionTime.Time,
		})
	}
	return falseConditionsSince(views)
}

func daemonSetFalseConditions(conds []appsv1.DaemonSetCondition) (time.Time, []string) {
	views := make([]condView, 0, len(conds))
	for _, c := range conds {
		views = append(views, condView{
			Type: string(c.Type), Status: c.Status, Reason: c.Reason, Message: c.Message,
			LastTransition: c.LastTransitionTime.Time,
		})
	}
	return falseConditionsSince(views)
}

func podContainersReady(p corev1.Pod) bool {
	ready, total := podReadyCounts(p)
	return total > 0 && ready == total
}

func podReadyCounts(p corev1.Pod) (ready, total int32) {
	for _, cs := range p.Status.ContainerStatuses {
		total++
		if cs.Ready {
			ready++
		}
	}
	return ready, total
}

func pendingHint(p corev1.Pod) (reason, msg string) {
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
			r := cond.Reason
			if r == "" {
				r = "Pending"
			}
			m := cond.Message
			if m == "" {
				m = r
			}
			return r, m
		}
	}
	for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			m := w.Message
			if m == "" {
				m = w.Reason
			}
			return w.Reason, m
		}
	}
	if p.Status.Message != "" {
		return "Pending", p.Status.Message
	}
	return "Pending", "Pod 仍处于 Pending"
}

func nodeInternalIP(n corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func formatAgeAt(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func fmtCores(v float64) string {
	if v < 1 {
		return fmt.Sprintf("%.0fm", v*1000)
	}
	return fmt.Sprintf("%.2f", v)
}

func fmtBytes(v float64) string {
	gi := 1024 * 1024 * 1024
	mi := 1024 * 1024
	if v >= float64(gi) {
		return fmt.Sprintf("%.2fGi", v/float64(gi))
	}
	return fmt.Sprintf("%.0fMi", v/float64(mi))
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func clipEvidence(ev []string) []string {
	out := make([]string, 0, len(ev))
	for _, s := range ev {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, clipRunes(s, diagnoseMsgMaxRunes))
		if len(out) >= diagnoseEvidenceMax {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func appendWarningEvidence(ev []string, line string) []string {
	line = clipRunes(strings.TrimSpace(line), diagnoseMsgMaxRunes)
	if line == "" {
		return ev
	}
	if warningEvidenceCount(ev) >= diagnoseEventPerIssue {
		return ev
	}
	if len(ev) < diagnoseEvidenceMax {
		return append(ev, line)
	}
	for i := len(ev) - 1; i >= 0; i-- {
		if strings.HasPrefix(ev[i], "Warning ") || isRestartEvidenceLine(ev[i]) {
			continue
		}
		ev[i] = line
		return ev
	}
	return ev
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func appendCapped(dst *[]Issue, add []Issue, max int) bool {
	if max <= 0 {
		return len(add) > 0
	}
	remain := max - len(*dst)
	if remain <= 0 {
		return true
	}
	trunc := false
	if len(add) > remain {
		add = add[:remain]
		trunc = true
	}
	*dst = append(*dst, add...)
	return trunc
}

func capCategory(items *[]Issue, max int) bool {
	if len(*items) <= max {
		return false
	}
	*items = (*items)[:max]
	return true
}

func keepTopIssues(items *[]Issue, max int) bool {
	sortIssues(*items)
	return capCategory(items, max)
}

func countIssues(items []Issue) DiagnoseCounts {
	var c DiagnoseCounts
	for _, it := range items {
		switch it.Category {
		case "node":
			c.Nodes++
		case "workload":
			c.Workloads++
		case "pod":
			c.Pods++
		case "event":
			c.Events++
		case "storage":
			c.Storage++
		}
		if it.Severity == "critical" {
			c.Critical++
		} else if it.Severity == "error" {
			c.Error++
		} else if it.Severity == "info" {
			c.Info++
		} else {
			c.Warning++
		}
	}
	return c
}

func sortIssues(items []Issue) {
	sev := map[string]int{"critical": 0, "error": 1, "warning": 2, "info": 3}
	cat := map[string]int{"node": 0, "workload": 1, "pod": 2, "storage": 3, "event": 4}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if sev[a.Severity] != sev[b.Severity] {
			return sev[a.Severity] < sev[b.Severity]
		}
		if cat[a.Category] != cat[b.Category] {
			return cat[a.Category] < cat[b.Category]
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Reason < b.Reason
	})
}

func isNoAccess(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
}

func diagnoseListOpts(cont string, limit int64, fieldSelector string) metav1.ListOptions {
	opts := metav1.ListOptions{Limit: limit, Continue: cont}
	if fieldSelector != "" {
		opts.FieldSelector = fieldSelector
	}
	// Continue 与 resourceVersion 不能同时设；首页走 watch cache，减轻 etcd 压力。
	if cont == "" {
		opts.ResourceVersion = "0"
	}
	return opts
}

func doDiagnoseList[T any](opt DiagnoseOptions, cont string, limit int64, selector string, fn func(metav1.ListOptions) ([]T, string, error)) ([]T, string, error) {
	opts := diagnoseListOpts(cont, limit, selector)
	items, next, err := fn(opts)
	if err == nil || cont != "" || isNoAccess(err) {
		return items, next, err
	}
	firstErr := err
	opts.ResourceVersion = ""
	items, next, err = fn(opts)
	if err == nil {
		if opt.OnCacheMiss != nil {
			opt.OnCacheMiss()
		}
		return items, next, nil
	}
	if selector != "" {
		opts.FieldSelector = ""
		items, next, err = fn(opts)
		if err == nil {
			if opt.OnCacheMiss != nil {
				opt.OnCacheMiss()
			}
			return items, next, nil
		}
	}
	return nil, "", firstErr
}

func (c *Clients) namespaceNames(ctx context.Context, opt DiagnoseOptions) ([]string, error) {
	items, _, err := doDiagnoseList(opt, "", 500, "", func(opts metav1.ListOptions) ([]corev1.Namespace, string, error) {
		list, err := c.Clientset.CoreV1().Namespaces().List(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		return list.Items, list.Continue, nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, n := range items {
		out = append(out, n.Name)
	}
	return out, nil
}

func (c *Clients) listPodsDiagnose(ctx context.Context, opt DiagnoseOptions) ([]corev1.Pod, bool, error) {
	return listNamespaced(ctx, c, opt, func(ns string) ([]corev1.Pod, bool, error) {
		return listPaged(ctx, diagnoseMaxScan, func(cont string, limit int64) ([]corev1.Pod, string, error) {
			return doDiagnoseList(opt, cont, limit, "status.phase!=Succeeded", func(opts metav1.ListOptions) ([]corev1.Pod, string, error) {
				list, err := c.Clientset.CoreV1().Pods(ns).List(ctx, opts)
				if err != nil {
					return nil, "", err
				}
				return list.Items, list.Continue, nil
			})
		})
	})
}

func (c *Clients) scanWorkloads(ctx context.Context, opt DiagnoseOptions, addScan func(func(*DiagnoseScan))) ([]Issue, []workloadRef, bool, error) {
	deps, dTrunc, err := c.listDeploymentsDiagnose(ctx, opt)
	if err != nil {
		return nil, nil, false, err
	}
	sts, sTrunc, err := c.listStatefulSetsDiagnose(ctx, opt)
	if err != nil {
		return nil, nil, false, err
	}
	dss, dsTrunc, err := c.listDaemonSetsDiagnose(ctx, opt)
	if err != nil {
		return nil, nil, false, err
	}
	addScan(func(s *DiagnoseScan) {
		s.Deployments = len(deps)
		s.StatefulSets = len(sts)
		s.DaemonSets = len(dss)
	})
	var issues []Issue
	var refs []workloadRef
	for _, d := range deps {
		issues = append(issues, diagnoseDeployment(d, opt.Now)...)
		if r, ok := newWorkloadRef("Deployment", d.Namespace, d.Name, d.CreationTimestamp.Time, d.Spec.Selector); ok {
			refs = append(refs, r)
		}
	}
	for _, s := range sts {
		issues = append(issues, diagnoseStatefulSet(s, opt.Now)...)
		if r, ok := newWorkloadRef("StatefulSet", s.Namespace, s.Name, s.CreationTimestamp.Time, s.Spec.Selector); ok {
			refs = append(refs, r)
		}
	}
	for _, ds := range dss {
		issues = append(issues, diagnoseDaemonSet(ds, opt.Now)...)
		if r, ok := newWorkloadRef("DaemonSet", ds.Namespace, ds.Name, ds.CreationTimestamp.Time, ds.Spec.Selector); ok {
			refs = append(refs, r)
		}
	}
	return issues, refs, dTrunc || sTrunc || dsTrunc, nil
}

func (c *Clients) listDeploymentsDiagnose(ctx context.Context, opt DiagnoseOptions) ([]appsv1.Deployment, bool, error) {
	return listNamespaced(ctx, c, opt, func(ns string) ([]appsv1.Deployment, bool, error) {
		return listPaged(ctx, diagnoseMaxScan, func(cont string, limit int64) ([]appsv1.Deployment, string, error) {
			return doDiagnoseList(opt, cont, limit, "", func(opts metav1.ListOptions) ([]appsv1.Deployment, string, error) {
				list, err := c.Clientset.AppsV1().Deployments(ns).List(ctx, opts)
				if err != nil {
					return nil, "", err
				}
				return list.Items, list.Continue, nil
			})
		})
	})
}

func (c *Clients) listStatefulSetsDiagnose(ctx context.Context, opt DiagnoseOptions) ([]appsv1.StatefulSet, bool, error) {
	return listNamespaced(ctx, c, opt, func(ns string) ([]appsv1.StatefulSet, bool, error) {
		return listPaged(ctx, diagnoseMaxScan, func(cont string, limit int64) ([]appsv1.StatefulSet, string, error) {
			return doDiagnoseList(opt, cont, limit, "", func(opts metav1.ListOptions) ([]appsv1.StatefulSet, string, error) {
				list, err := c.Clientset.AppsV1().StatefulSets(ns).List(ctx, opts)
				if err != nil {
					return nil, "", err
				}
				return list.Items, list.Continue, nil
			})
		})
	})
}

func (c *Clients) listDaemonSetsDiagnose(ctx context.Context, opt DiagnoseOptions) ([]appsv1.DaemonSet, bool, error) {
	return listNamespaced(ctx, c, opt, func(ns string) ([]appsv1.DaemonSet, bool, error) {
		return listPaged(ctx, diagnoseMaxScan, func(cont string, limit int64) ([]appsv1.DaemonSet, string, error) {
			return doDiagnoseList(opt, cont, limit, "", func(opts metav1.ListOptions) ([]appsv1.DaemonSet, string, error) {
				list, err := c.Clientset.AppsV1().DaemonSets(ns).List(ctx, opts)
				if err != nil {
					return nil, "", err
				}
				return list.Items, list.Continue, nil
			})
		})
	})
}

func (c *Clients) listWarningEvents(ctx context.Context, opt DiagnoseOptions) ([]corev1.Event, bool, error) {
	listFn := func(ns string) ([]corev1.Event, bool, error) {
		return listPaged(ctx, diagnoseEventScan, func(cont string, limit int64) ([]corev1.Event, string, error) {
			return doDiagnoseList(opt, cont, limit, "type=Warning", func(opts metav1.ListOptions) ([]corev1.Event, string, error) {
				list, err := c.Clientset.CoreV1().Events(ns).List(ctx, opts)
				if err != nil {
					return nil, "", err
				}
				return list.Items, list.Continue, nil
			})
		})
	}
	items, trunc, err := listNamespaced(ctx, c, opt, listFn)
	if err != nil {
		return nil, false, err
	}
	sort.Slice(items, func(i, j int) bool {
		return eventTime(items[i]).After(eventTime(items[j]))
	})
	if len(items) > diagnoseMaxEvents {
		items = items[:diagnoseMaxEvents]
		trunc = true
	}
	return items, trunc, nil
}

func (c *Clients) listNodesPaged(ctx context.Context, opt DiagnoseOptions, max int) ([]corev1.Node, bool, error) {
	return listPaged(ctx, max, func(cont string, limit int64) ([]corev1.Node, string, error) {
		return doDiagnoseList(opt, cont, limit, "", func(opts metav1.ListOptions) ([]corev1.Node, string, error) {
			list, err := c.Clientset.CoreV1().Nodes().List(ctx, opts)
			if err != nil {
				return nil, "", err
			}
			return list.Items, list.Continue, nil
		})
	})
}

func (c *Clients) listNodeMetricsPaged(ctx context.Context, max int) ([]metricsv1beta1.NodeMetrics, bool, error) {
	// metrics-server 通常不接受 resourceVersion=0，强行带上会导致用量证据整段缺失。
	return listPaged(ctx, max, func(cont string, limit int64) ([]metricsv1beta1.NodeMetrics, string, error) {
		list, err := c.Metrics.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{Limit: limit, Continue: cont})
		if err != nil {
			return nil, "", err
		}
		return list.Items, list.Continue, nil
	})
}

func listNamespaced[T any](ctx context.Context, c *Clients, opt DiagnoseOptions, fn func(ns string) ([]T, bool, error)) ([]T, bool, error) {
	if ns := strings.TrimSpace(opt.Namespace); ns != "" {
		return fn(ns)
	}
	if opt.PermType != "project" {
		items, trunc, err := fn("")
		if err == nil {
			return items, trunc, nil
		}
		if !isNoAccess(err) {
			return nil, false, err
		}
		nss := opt.FallbackNS
		if len(nss) == 0 && c != nil {
			if names, nsErr := c.namespaceNames(ctx, opt); nsErr == nil {
				nss = names
			}
		}
		if len(nss) == 0 {
			return nil, false, err
		}
		return listAcrossNS(ctx, nss, diagnoseMaxScan, fn)
	}
	nss := opt.FallbackNS
	if len(nss) == 0 && c != nil {
		if names, nsErr := c.namespaceNames(ctx, opt); nsErr == nil {
			nss = names
		}
	}
	if len(nss) == 0 {
		return nil, false, fmt.Errorf("no namespaces available for project kubeconfig")
	}
	return listAcrossNS(ctx, nss, diagnoseMaxScan, fn)
}

func listPaged[T any](ctx context.Context, max int, fn func(continueToken string, limit int64) ([]T, string, error)) ([]T, bool, error) {
	var all []T
	cont := ""
	truncated := false
	for {
		if err := ctx.Err(); err != nil {
			return all, truncated, err
		}
		remain := max - len(all)
		if remain <= 0 {
			return all, true, nil
		}
		limit := int64(diagnoseListPage)
		if remain < diagnoseListPage {
			limit = int64(remain)
		}
		items, next, err := fn(cont, limit)
		if err != nil {
			return all, truncated, err
		}
		all = append(all, items...)
		if next == "" {
			break
		}
		if len(all) >= max {
			truncated = true
			break
		}
		cont = next
	}
	return all, truncated, nil
}

func listAcrossNS[T any](ctx context.Context, nss []string, maxTotal int, fn func(ns string) ([]T, bool, error)) ([]T, bool, error) {
	if len(nss) == 0 {
		return nil, false, nil
	}
	type part struct {
		items []T
		trunc bool
		err   error
	}
	parts := make([]part, len(nss))
	sem := make(chan struct{}, diagnoseNSWorkers)
	var wg sync.WaitGroup
	for i, ns := range nss {
		wg.Add(1)
		go func(i int, ns string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				parts[i].err = ctx.Err()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			items, trunc, err := fn(ns)
			parts[i] = part{items: items, trunc: trunc, err: err}
		}(i, ns)
	}
	wg.Wait()

	var (
		out   []T
		trunc bool
		errs  []string
	)
	for _, p := range parts {
		if p.err != nil {
			if isNoAccess(p.err) {
				continue
			}
			errs = append(errs, p.err.Error())
			continue
		}
		if p.trunc {
			trunc = true
		}
		remain := maxTotal - len(out)
		if remain <= 0 {
			trunc = true
			break
		}
		if len(p.items) > remain {
			out = append(out, p.items[:remain]...)
			trunc = true
			break
		}
		out = append(out, p.items...)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, trunc, fmt.Errorf("%s", strings.Join(uniq(errs), "; "))
	}
	return out, trunc, nil
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
