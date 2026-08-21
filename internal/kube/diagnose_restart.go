package kube

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type workloadRef struct {
	Kind, Namespace, Name string
	Selector              labels.Selector
	SelectorStr           string
	Created               time.Time
	RolloutRecent         bool
}

type restartHit struct {
	When      time.Time
	Pod       string
	Container string
	Reason    string
	Exit      int32
	Count     int32
	Source    string
	hasExit   bool
}

func newWorkloadRef(kind, ns, name string, created time.Time, ls *metav1.LabelSelector) (workloadRef, bool) {
	if ls == nil {
		return workloadRef{}, false
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil || sel == nil || sel.Empty() {
		return workloadRef{}, false
	}
	s := metav1.FormatLabelSelector(ls)
	if s == "" || s == "<none>" || s == "<error>" {
		s = sel.String()
	}
	return workloadRef{
		Kind: kind, Namespace: ns, Name: name,
		Selector: sel, SelectorStr: s, Created: created,
	}, true
}

func (w workloadRef) owns(p corev1.Pod) bool {
	if w.Selector == nil || p.Namespace != w.Namespace {
		return false
	}
	return w.Selector.Matches(labels.Set(p.Labels))
}

func attachRestartHistory(issues []Issue, pods []corev1.Pod, workloads []workloadRef, events []corev1.Event, now time.Time) []Issue {
	podByKey := map[string]corev1.Pod{}
	for _, p := range pods {
		podByKey[issueKey("Pod", p.Namespace, p.Name)] = p
	}

	for i := range issues {
		it := &issues[i]
		if it.Category == "pod" && strings.EqualFold(it.Kind, "Pod") {
			p, ok := podByKey[issueKey("Pod", it.Namespace, it.Name)]
			if !ok {
				continue
			}
			allHits := collectPodRestartHits(p, events, now)
			hits := topRestartHits(allHits, now)
			if len(hits) == 0 && strings.EqualFold(it.Reason, "Restarts") {
				if h, ok := latestRestartHit(allHits, now, diagnoseRestartDetail); ok {
					hits = []restartHit{h}
				}
			}
			if len(hits) == 0 {
				continue
			}
			it.Evidence = applyRestartEvidence(it.Evidence, hits, "", now)
		}
	}

	for _, w := range workloads {
		var owned []corev1.Pod
		var hits []restartHit
		worthy := 0
		for _, p := range pods {
			if !w.owns(p) {
				continue
			}
			owned = append(owned, p)
			hits = append(hits, collectPodRestartHits(p, events, now)...)
			if podRestartWorthy(p, now) {
				worthy++
			}
		}
		top := topRestartHits(hits, now)
		latest, hasLatest := latestRestartHit(hits, now, diagnoseRestartDetail)
		if len(top) == 0 && !hasLatest {
			continue
		}
		idx := findWorkloadIssue(issues, w)
		if idx >= 0 {
			if len(top) > 0 {
				issues[idx].Evidence = applyRestartEvidence(issues[idx].Evidence, top, w.SelectorStr, now)
			} else {
				issues[idx].Evidence = applyRestartEvidence(issues[idx].Evidence, []restartHit{latest}, w.SelectorStr, now)
			}
			continue
		}
		if worthy == 0 || w.RolloutRecent {
			continue
		}
		issues = append(issues, newWorkloadRestartsIssue(w, owned, top, latest, hasLatest, now))
	}
	return issues
}

func findWorkloadIssue(issues []Issue, w workloadRef) int {
	key := issueKey(w.Kind, w.Namespace, w.Name)
	for i := range issues {
		it := issues[i]
		if it.Category != "workload" {
			continue
		}
		if issueKey(it.Kind, it.Namespace, it.Name) == key {
			return i
		}
	}
	return -1
}

func newWorkloadRestartsIssue(w workloadRef, owned []corev1.Pod, recent []restartHit, latest restartHit, hasLatest bool, now time.Time) Issue {
	nPods := restartPodCount(append([]restartHit{}, recent...))
	if nPods == 0 && hasLatest {
		nPods = restartPodCount([]restartHit{latest})
	}
	totalRestarts := workloadRestartCount(owned)
	// totalRestarts 是生命周期 RestartCount 合计，不是 2h 窗口内次数，措辞必须分开写。
	msg := fmt.Sprintf("负载累计重启 %d 次", totalRestarts)
	evidenceHits := recent
	if len(evidenceHits) == 0 && hasLatest {
		evidenceHits = []restartHit{latest}
	}
	if len(recent) > 0 {
		latest = recent[0]
		hasLatest = true
	}
	if hasLatest && !latest.When.IsZero() && now.Sub(latest.When) <= diagnoseRestartRecent {
		msg = fmt.Sprintf("近 2h 内有 %d 个 Pod 重启 · 负载累计重启 %d 次 · 最近 %s", nPods, totalRestarts, formatRestartMoment(latest, now))
	} else if hasLatest && !latest.When.IsZero() {
		from := latest.Pod
		if from == "" {
			from = "下属 Pod"
		}
		msg = fmt.Sprintf("近 2h 内无重启 · 负载累计重启 %d 次 · 最近一次来自 Pod %s：%s", totalRestarts, from, formatRestartMoment(latest, now))
	} else {
		msg = fmt.Sprintf("负载累计重启 %d 次，但最近一次精确重启时间缺失（仅拿到聚合重启迹象）", totalRestarts)
	}
	age := w.Created
	if age.IsZero() && len(owned) > 0 {
		age = owned[0].CreationTimestamp.Time
	}
	it := Issue{
		Severity:  "warning",
		Category:  "workload",
		Kind:      w.Kind,
		Namespace: w.Namespace,
		Name:      w.Name,
		Reason:    "Restarts",
		Message:   clipRunes(msg, diagnoseMsgMaxRunes),
		Age:       formatAgeAt(age, now),
		Evidence:  applyRestartEvidence(nil, evidenceHits, w.SelectorStr, now),
	}
	if hasLatest {
		stampIssueSince(&it, latest.When, now)
	}
	return it
}

func workloadRestartCount(pods []corev1.Pod) int32 {
	var total int32
	for _, p := range pods {
		for _, cs := range p.Status.InitContainerStatuses {
			total += cs.RestartCount
		}
		for _, cs := range p.Status.ContainerStatuses {
			total += cs.RestartCount
		}
	}
	return total
}

// formatPodRestartMessage 汇总 Pod 级 Restarts 摘要。
// restarts 是生命周期累计次数；是否“近 2h 有重启”只由 last 相对 now 判断，二者不可混写成“近 2h 累计”。
func formatPodRestartMessage(p corev1.Pod, restarts int32, last, now time.Time) string {
	reason := lastRestartReason(p)
	exit, hasExit := lastRestartExit(p)
	age := formatAgeAt(last, now)
	moment := formatRestartMomentParts(reason, exit, hasExit, age, last)
	if !last.IsZero() && now.Sub(last) <= diagnoseRestartRecent {
		if moment != "" {
			return fmt.Sprintf("近 2h 内有重启 · 累计重启 %d 次 · 最近 %s", restarts, moment)
		}
		return fmt.Sprintf("近 2h 内有重启 · 累计重启 %d 次 · 最近一次精确时间缺失（仅拿到累计次数）", restarts)
	}
	if moment != "" {
		return fmt.Sprintf("近 2h 内无重启 · 累计重启 %d 次 · 最近一次 %s", restarts, moment)
	}
	if restarts > 0 {
		return fmt.Sprintf("累计重启 %d 次，但最近一次精确时间缺失（Pod status / Warning 事件未保留）", restarts)
	}
	return "最近一次精确时间缺失（Pod status / Warning 事件未保留）"
}

func lastRestartReason(p corev1.Pod) string {
	var latest time.Time
	var reason string
	consider := func(t *corev1.ContainerStateTerminated) {
		if t == nil || t.FinishedAt.IsZero() {
			return
		}
		if t.FinishedAt.After(latest) {
			latest = t.FinishedAt.Time
			reason = t.Reason
		}
	}
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		consider(cs.State.Terminated)
		consider(cs.LastTerminationState.Terminated)
	}
	return reason
}

func lastRestartExit(p corev1.Pod) (int32, bool) {
	var latest time.Time
	var exit int32
	var ok bool
	consider := func(t *corev1.ContainerStateTerminated) {
		if t == nil || t.FinishedAt.IsZero() {
			return
		}
		if t.FinishedAt.After(latest) {
			latest = t.FinishedAt.Time
			exit = t.ExitCode
			ok = true
		}
	}
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		consider(cs.State.Terminated)
		consider(cs.LastTerminationState.Terminated)
	}
	return exit, ok
}

func formatRestartMoment(h restartHit, now time.Time) string {
	return formatRestartMomentParts(h.Reason, h.Exit, h.hasExit, formatAgeAt(h.When, now), h.When)
}

func formatRestartMomentParts(reason string, exit int32, hasExit bool, age string, when time.Time) string {
	var b strings.Builder
	if reason != "" {
		b.WriteString(reason)
	}
	if hasExit {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "exit=%d", exit)
	}
	if age != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(age)
		b.WriteString(" ago")
	}
	if !when.IsZero() {
		b.WriteString("（")
		b.WriteString(formatExactTime(when))
		b.WriteString("）")
	}
	return b.String()
}

func restartPodCount(hits []restartHit) int {
	seen := map[string]bool{}
	for _, h := range hits {
		if h.Pod == "" {
			continue
		}
		seen[h.Pod] = true
	}
	if len(seen) == 0 {
		return len(hits)
	}
	return len(seen)
}

func podRestartWorthy(p corev1.Pod, now time.Time) bool {
	if _, _, crit := podContainerProblem(p); crit {
		return true
	}
	var restarts int32
	for _, cs := range p.Status.ContainerStatuses {
		restarts += cs.RestartCount
	}
	if restarts < diagnoseRestartWarn {
		return false
	}
	last := lastRestartAt(p)
	if last.IsZero() {
		return true
	}
	return now.Sub(last) <= diagnoseRestartDetail
}

func collectPodRestartHits(p corev1.Pod, events []corev1.Event, now time.Time) []restartHit {
	var hits []restartHit
	addTerm := func(cs corev1.ContainerStatus, t *corev1.ContainerStateTerminated) {
		if t == nil || t.FinishedAt.IsZero() {
			return
		}
		hits = append(hits, restartHit{
			When:      t.FinishedAt.Time,
			Pod:       p.Name,
			Container: cs.Name,
			Reason:    t.Reason,
			Exit:      t.ExitCode,
			hasExit:   true,
			Source:    "status.lastState",
		})
	}
	addStatus := func(cs corev1.ContainerStatus) {
		if cs.RestartCount <= 0 && cs.State.Terminated == nil {
			return
		}
		addTerm(cs, cs.State.Terminated)
		addTerm(cs, cs.LastTerminationState.Terminated)
	}
	for _, cs := range p.Status.InitContainerStatuses {
		addStatus(cs)
	}
	for _, cs := range p.Status.ContainerStatuses {
		addStatus(cs)
	}
	for _, e := range events {
		if !isRestartTimelineEvent(e, p) {
			continue
		}
		when := eventTime(e)
		if when.IsZero() {
			continue
		}
		hits = append(hits, restartHit{
			When:      when,
			Pod:       p.Name,
			Container: containerFromFieldPath(e.InvolvedObject.FieldPath),
			Reason:    e.Reason,
			Count:     e.Count,
			Source:    "event",
		})
	}
	return hits
}

func isRestartTimelineEvent(e corev1.Event, p corev1.Pod) bool {
	if !strings.EqualFold(e.InvolvedObject.Kind, "Pod") {
		return false
	}
	ns := firstNonEmpty(e.InvolvedObject.Namespace, e.Namespace)
	if ns != p.Namespace || e.InvolvedObject.Name != p.Name {
		return false
	}
	switch e.Reason {
	case "Killing", "BackOff":
		return true
	default:
		return false
	}
}

func topRestartHits(hits []restartHit, now time.Time) []restartHit {
	var fresh []restartHit
	for _, h := range hits {
		if h.When.IsZero() || now.Sub(h.When) > diagnoseRestartRecent {
			continue
		}
		fresh = append(fresh, h)
	}
	if len(fresh) == 0 {
		return nil
	}
	sort.SliceStable(fresh, func(i, j int) bool {
		if !fresh[i].When.Equal(fresh[j].When) {
			return fresh[i].When.After(fresh[j].When)
		}
		return restartSourceRank(fresh[i]) > restartSourceRank(fresh[j])
	})
	var out []restartHit
	for _, h := range fresh {
		dup := false
		for _, e := range out {
			if e.Pod == h.Pod && sameRestartContainer(e.Container, h.Container) && absDuration(e.When, h.When) < diagnoseRestartDedup {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		out = append(out, h)
		if len(out) >= diagnoseRestartMax {
			break
		}
	}
	return out
}

func latestRestartHit(hits []restartHit, now time.Time, maxAge time.Duration) (restartHit, bool) {
	var filtered []restartHit
	for _, h := range hits {
		if h.When.IsZero() || now.Sub(h.When) > maxAge {
			continue
		}
		filtered = append(filtered, h)
	}
	if len(filtered) == 0 {
		return restartHit{}, false
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if !filtered[i].When.Equal(filtered[j].When) {
			return filtered[i].When.After(filtered[j].When)
		}
		return restartSourceRank(filtered[i]) > restartSourceRank(filtered[j])
	})
	return filtered[0], true
}

func sameRestartContainer(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	return a == b
}

func restartSourceRank(h restartHit) int {
	switch h.Source {
	case "status.lastState":
		return 3
	case "event":
		if h.Reason == "Killing" {
			return 2
		}
		return 1
	default:
		return 0
	}
}

func absDuration(a, b time.Time) time.Duration {
	if a.After(b) {
		return a.Sub(b)
	}
	return b.Sub(a)
}

func applyRestartEvidence(ev []string, hits []restartHit, selector string, now time.Time) []string {
	head := formatRestartHits(hits, now)
	if n := restartNote(len(hits)); n != "" {
		head = append(head, n)
	}
	if selector = strings.TrimSpace(selector); selector != "" {
		head = append(head, "selector="+selector)
	}
	return prependRestartEvidence(ev, head, "")
}

func formatRestartHits(hits []restartHit, now time.Time) []string {
	out := make([]string, 0, len(hits))
	for i, h := range hits {
		out = append(out, formatRestartHit(h, i+1, now))
	}
	return out
}

func formatRestartHit(h restartHit, idx int, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "restart[%d] %s ago", idx, formatAgeAt(h.When, now))
	if !h.When.IsZero() && now.Sub(h.When) <= diagnoseRestartDetail {
		fmt.Fprintf(&b, " at=%s", formatExactTime(h.When))
	}
	if h.Pod != "" {
		fmt.Fprintf(&b, " pod=%s", h.Pod)
	}
	if h.Container != "" {
		fmt.Fprintf(&b, " container=%s", h.Container)
	}
	if h.Reason != "" {
		fmt.Fprintf(&b, " %s", h.Reason)
	}
	if h.hasExit {
		fmt.Fprintf(&b, " exit=%d", h.Exit)
	}
	if h.Count > 1 {
		fmt.Fprintf(&b, " ×%d", h.Count)
	}
	if h.Source != "" {
		fmt.Fprintf(&b, " · %s", h.Source)
	}
	return b.String()
}

func formatExactTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	cst := time.FixedZone("UTC+8", 8*60*60)
	return t.In(cst).Format("2006-01-02 15:04:05")
}

func restartNote(n int) string {
	if n >= diagnoseRestartMax {
		return ""
	}
	return fmt.Sprintf("restartNote: 集群只保留上次退出；更早次数来自 Warning 事件，本对象只确认 %d 条", n)
}

func prependRestartEvidence(ev []string, lines []string, note string) []string {
	head := make([]string, 0, len(lines)+2)
	for _, s := range lines {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		head = append(head, s)
	}
	if note = strings.TrimSpace(note); note != "" {
		head = append(head, note)
	}
	if len(head) == 0 {
		return clipEvidence(ev)
	}
	var rest []string
	for _, s := range ev {
		s = strings.TrimSpace(s)
		if s == "" || isRestartEvidenceLine(s) {
			continue
		}
		rest = append(rest, s)
	}
	return clipEvidence(append(head, rest...))
}

func isRestartEvidenceLine(s string) bool {
	return strings.HasPrefix(s, "restart[") || strings.HasPrefix(s, "restartNote:") || strings.HasPrefix(s, "selector=")
}

func hasRestartTimelineEvidence(ev []string) bool {
	for _, s := range ev {
		if strings.HasPrefix(s, "restart[") {
			return true
		}
	}
	return false
}
