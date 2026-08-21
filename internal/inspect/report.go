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

func countIssues(issues []kube.Issue) (critical, errorN, warning, info int) {
	for _, it := range issues {
		switch it.Severity {
		case "critical":
			critical++
		case "error":
			errorN++
		case "info":
			info++
		default:
			warning++
		}
	}
	return critical, errorN, warning, info
}

func sortIssues(issues []kube.Issue) {
	rank := func(sev string) int {
		switch sev {
		case "critical":
			return 0
		case "error":
			return 1
		case "warning":
			return 2
		case "info":
			return 3
		default:
			return 2
		}
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

func makeSummary(probe kube.ProbeResult, critical, errorN, warning, info int) string {
	if probe.Status != "ok" {
		return "kubeconfig 无效"
	}
	if critical == 0 && errorN == 0 && warning == 0 {
		if info > 0 {
			return strconv.Itoa(info) + " 提示"
		}
		return "巡检正常"
	}
	parts := make([]string, 0, 3)
	if critical > 0 {
		parts = append(parts, strconv.Itoa(critical)+" 严重")
	}
	if errorN > 0 {
		parts = append(parts, strconv.Itoa(errorN)+" 错误")
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
			Since:     it.Since,
		})
	}
	return out
}
