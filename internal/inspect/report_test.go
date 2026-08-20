package inspect

import (
	"strings"
	"testing"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
)

func TestKubeconfigIssueAndSummary(t *testing.T) {
	cl := &model.Cluster{Name: "prod"}
	probe := kube.ProbeResult{Status: "error", LastError: "x509: certificate"}
	iss := kubeconfigIssue(cl, probe)
	if iss.Severity != "critical" || iss.Category != "kubeconfig" || !strings.Contains(iss.Message, "x509") {
		t.Fatalf("issue: %+v", iss)
	}
	if got := makeSummary(probe, 1, 0); got != "kubeconfig 无效" {
		t.Fatalf("summary %q", got)
	}
	ok := kube.ProbeResult{Status: "ok"}
	if got := makeSummary(ok, 0, 0); got != "巡检正常" {
		t.Fatalf("ok summary %q", got)
	}
	if got := makeSummary(ok, 2, 3); got != "2 严重 / 3 警告" {
		t.Fatalf("count summary %q", got)
	}
}

func TestSortAndPreview(t *testing.T) {
	issues := []kube.Issue{
		{Severity: "warning", Name: "b", Message: strings.Repeat("m", 200)},
		{Severity: "critical", Name: "a", Namespace: "ns", Reason: "CrashLoopBackOff"},
		{Severity: "critical", Name: "c"},
	}
	sortIssues(issues)
	if issues[0].Name != "c" || issues[1].Name != "a" || issues[2].Name != "b" {
		t.Fatalf("order: %+v", issues)
	}
	prev := previewOf(issues, 2)
	if len(prev) != 2 || prev[0].Name != "c" || prev[1].Name != "a" {
		t.Fatalf("preview %+v", prev)
	}
	long := clipRunes(strings.Repeat("汉", PreviewClip+2), PreviewClip)
	if !strings.HasSuffix(long, "…") || len([]rune(long)) != PreviewClip+1 {
		t.Fatalf("rune clip %q runes=%d", long, len([]rune(long)))
	}
}
