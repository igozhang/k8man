package inspect

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
)

// Report is the JSON blob stored with each inspection row.
type Report struct {
	Probe    kube.ProbeResult     `json:"probe"`
	Diagnose *kube.DiagnoseReport `json:"diagnose,omitempty"`
	Issues   []kube.Issue         `json:"issues"`
}

func kubeconfigIssue(cl *model.Cluster, probe kube.ProbeResult) kube.Issue {
	msg := strings.TrimSpace(probe.LastError)
	if msg == "" {
		msg = "kubeconfig 无法连通"
	}
	return kube.Issue{
		Severity: "critical",
		Category: "kubeconfig",
		Kind:     "Kubeconfig",
		Name:     cl.Name,
		Reason:   "Unreachable",
		Message:  msg,
	}
}

func clientIssue(cl *model.Cluster, err error) kube.Issue {
	msg := kube.FriendlyConnectivityError(err)
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return kube.Issue{
		Severity: "critical",
		Category: "kubeconfig",
		Kind:     "Kubeconfig",
		Name:     cl.Name,
		Reason:   "ClientError",
		Message:  msg,
	}
}

func diagnoseFailIssue(cl *model.Cluster, err error) kube.Issue {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return kube.Issue{
		Severity: "warning",
		Category: "event",
		Kind:     "Diagnose",
		Name:     cl.Name,
		Reason:   "DiagnoseError",
		Message:  msg,
	}
}

func countIssues(issues []kube.Issue) (critical, warning int) {
	for _, it := range issues {
		if it.Severity == "critical" {
			critical++
			continue
		}
		warning++
	}
	return critical, warning
}

func sortIssues(issues []kube.Issue) {
	rank := func(sev string) int {
		if sev == "critical" {
			return 0
		}
		if sev == "warning" {
			return 1
		}
		return 2
	}
	sort.SliceStable(issues, func(i, j int) bool {
		ri, rj := rank(issues[i].Severity), rank(issues[j].Severity)
		if ri != rj {
			return ri < rj
		}
		if issues[i].Namespace != issues[j].Namespace {
			return issues[i].Namespace < issues[j].Namespace
		}
		return issues[i].Name < issues[j].Name
	})
}

func makeSummary(probe kube.ProbeResult, critical, warning int) string {
	if probe.Status != "ok" {
		return "kubeconfig 无效"
	}
	if critical == 0 && warning == 0 {
		return "巡检正常"
	}
	parts := make([]string, 0, 2)
	if critical > 0 {
		parts = append(parts, strconv.Itoa(critical)+" 严重")
	}
	if warning > 0 {
		parts = append(parts, strconv.Itoa(warning)+" 警告")
	}
	return strings.Join(parts, " / ")
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func previewOf(issues []kube.Issue, n int) []PreviewIssue {
	if n <= 0 || len(issues) == 0 {
		return []PreviewIssue{}
	}
	if n > len(issues) {
		n = len(issues)
	}
	out := make([]PreviewIssue, 0, n)
	for _, it := range issues[:n] {
		out = append(out, PreviewIssue{
			Severity:  it.Severity,
			Category:  it.Category,
			Kind:      it.Kind,
			Namespace: it.Namespace,
			Name:      it.Name,
			Reason:    it.Reason,
			Message:   clipRunes(it.Message, PreviewClip),
		})
	}
	return out
}
