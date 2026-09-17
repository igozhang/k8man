package alert

import (
	"strings"
	"testing"
	"time"

	"gitee.com/k8man/internal/kube"
)

func TestIsAlertable(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		it   kube.Issue
		want bool
	}{
		{
			name: "critical always",
			it:   kube.Issue{Severity: "critical", SinceAt: now.Add(-5 * time.Hour).Format(time.RFC3339)},
			want: true,
		},
		{
			name: "error within 2h",
			it:   kube.Issue{Severity: "error", SinceAt: now.Add(-30 * time.Minute).Format(time.RFC3339)},
			want: true,
		},
		{
			name: "error older than 2h",
			it:   kube.Issue{Severity: "error", SinceAt: now.Add(-3 * time.Hour).Format(time.RFC3339)},
			want: false,
		},
		{
			name: "error without sinceAt",
			it:   kube.Issue{Severity: "error"},
			want: true,
		},
		{
			name: "warning never",
			it:   kube.Issue{Severity: "warning", SinceAt: now.Add(-1 * time.Minute).Format(time.RFC3339)},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAlertable(tc.it, now); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestValidateWebhookURL(t *testing.T) {
	ok := "https://open.feishu.cn/open-apis/bot/v2/hook/e52319a0-09dc-40f1-8132-73d7df06af83"
	if err := ValidateWebhookURL(ok); err != nil {
		t.Fatalf("want ok: %v", err)
	}
	bads := []string{
		"",
		"http://open.feishu.cn/open-apis/bot/v2/hook/x",
		"https://evil.example/open-apis/bot/v2/hook/x",
		"https://open.feishu.cn/other",
	}
	for _, u := range bads {
		if err := ValidateWebhookURL(u); err == nil {
			t.Fatalf("want error for %q", u)
		}
	}
}

func TestIssueFingerprint(t *testing.T) {
	a := kube.Issue{Category: "pod", Kind: "Pod", Namespace: "ns", Name: "web", Reason: "CrashLoopBackOff", Severity: "critical"}
	b := a
	b.Severity = "error"
	if IssueFingerprint(a) != IssueFingerprint(b) {
		t.Fatal("fingerprint should ignore severity")
	}
}

func TestFormatMessage(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	msg := FormatMessage("prod", []kube.Issue{{
		Severity: "critical", Kind: "Pod", Namespace: "default", Name: "api", Reason: "CrashLoopBackOff", Message: "back-off",
	}}, now)
	if !containsAll(msg, "【k8man 告警】", "prod", "严重", "api", "CrashLoopBackOff") {
		t.Fatalf("unexpected message: %s", msg)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
