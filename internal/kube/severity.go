package kube

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	sevCritical = "critical"
	sevError    = "error"
	sevWarning  = "warning"
	sevInfo     = "info"

	severityDecay30 = 30 * 24 * time.Hour
	severityDecay60 = 60 * 24 * time.Hour
)

func severityRank(s string) int {
	switch s {
	case sevCritical:
		return 0
	case sevError:
		return 1
	case sevWarning:
		return 2
	case sevInfo:
		return 3
	default:
		return 2
	}
}

func severityName(rank int) string {
	switch {
	case rank <= 0:
		return sevCritical
	case rank == 1:
		return sevError
	case rank == 2:
		return sevWarning
	default:
		return sevInfo
	}
}

func stampIssueSince(it *Issue, t, now time.Time) {
	if it == nil {
		return
	}
	it.Since = formatAgeAt(t, now)
	if t.IsZero() {
		it.SinceAt = ""
		return
	}
	it.SinceAt = t.UTC().Format(time.RFC3339)
}

func parseIssueSinceAt(it Issue) (time.Time, bool) {
	s := strings.TrimSpace(it.SinceAt)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func isSystemNamespace(ns string) bool {
	ns = strings.ToLower(strings.TrimSpace(ns))
	switch ns {
	case "kube-system", "kube-public", "kube-node-lease", "istio-system", "istio-operator":
		return true
	}
	return strings.HasPrefix(ns, "cattle-") || strings.HasPrefix(ns, "calico-")
}

func isActiveFailure(it Issue) bool {
	switch it.Reason {
	case "CrashLoopBackOff", "OOMKilled":
		return true
	default:
		return false
	}
}

func severityNeverDecay(it Issue) bool {
	if it.Category == "kubeconfig" || it.Category == "event" {
		return true
	}
	switch it.Reason {
	case "CPUHigh", "MemoryHigh",
		string(corev1.NodeDiskPressure),
		string(corev1.NodeMemoryPressure),
		string(corev1.NodePIDPressure),
		string(corev1.NodeNetworkUnavailable),
		"Restarts", "DiagnoseError":
		return true
	default:
		return false
	}
}

func nodeReadyStatus(n corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type != corev1.NodeReady {
			continue
		}
		switch c.Status {
		case corev1.ConditionTrue:
			return "Ready"
		case corev1.ConditionFalse:
			return "NotReady"
		default:
			return "Unknown"
		}
	}
	return "Unknown"
}

func clusterNodeOutage(nodes []corev1.Node) bool {
	if len(nodes) == 0 {
		return false
	}
	bad := 0
	for _, n := range nodes {
		if nodeReadyStatus(n) != "Ready" {
			bad++
		}
	}
	ready := len(nodes) - bad
	return ready == 0 || bad*2 >= len(nodes)
}

func decayBySteps(base string, steps int, floor string) string {
	if steps <= 0 {
		return base
	}
	r := severityRank(base) + steps
	if r > severityRank(sevInfo) {
		r = severityRank(sevInfo)
	}
	fr := severityRank(floor)
	// 地板只拦「降得太轻」，不能把 warning 抬成 error。
	if severityRank(base) <= fr && r > fr {
		r = fr
	}
	return severityName(r)
}

func applySeverityDecay(issues []Issue, now time.Time, nodeOutage bool) {
	for i := range issues {
		applyOneSeverityDecay(&issues[i], now, nodeOutage)
	}
}

func applyOneSeverityDecay(it *Issue, now time.Time, nodeOutage bool) {
	if it == nil {
		return
	}
	if it.BaseSeverity == "" {
		it.BaseSeverity = it.Severity
	}
	if it.BaseSeverity == "" {
		it.BaseSeverity = sevWarning
		if it.Severity == "" {
			it.Severity = sevWarning
		}
	}
	if severityNeverDecay(*it) {
		return
	}
	if it.Category == "node" && (it.Reason == "NotReady" || it.Reason == "Unknown") && nodeOutage {
		return
	}
	since, ok := parseIssueSinceAt(*it)
	if !ok {
		return
	}
	age := now.Sub(since)
	steps := 0
	switch {
	case age >= severityDecay60:
		steps = 2
	case age >= severityDecay30:
		steps = 1
	}
	if steps == 0 {
		return
	}
	sys := isSystemNamespace(it.Namespace)
	if sys && steps > 1 {
		steps = 1
	}
	floor := sevInfo
	if sys || isActiveFailure(*it) {
		floor = sevError
	}
	got := decayBySteps(it.BaseSeverity, steps, floor)
	if got == it.Severity {
		return
	}
	it.Severity = got
	note := fmt.Sprintf("severityNote: 已持续 %s，%s → %s", formatAgeAt(since, now), it.BaseSeverity, got)
	ev := append([]string{}, it.Evidence...)
	if len(ev) >= diagnoseEvidenceMax {
		ev = ev[:diagnoseEvidenceMax-1]
	}
	it.Evidence = clipEvidence(append(ev, note))
}
