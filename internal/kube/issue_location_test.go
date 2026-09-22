package kube

import (
	"strings"
	"testing"
)

func TestEnrichNFSIssueMessageLegacy(t *testing.T) {
	it := Issue{
		Reason: "NFSHighUsage",
		NodeIP: "10.1.2.3",
		Evidence: []string{
			"server=10.1.2.3",
			"path=/export",
		},
		Message: "NFS 使用率 96%（763.10Gi / 797.85Gi），超过 80%",
	}
	got := EnrichIssueMessage(it)
	want := "10.1.2.3:/export"
	if !containsAll(got, want, "使用率 96%") {
		t.Fatalf("got %q", got)
	}
}

func TestEnrichNFSIssueMessageAlreadyHasServer(t *testing.T) {
	msg := "NFS 10.1.2.3:/export 使用率 96%（763.10Gi / 797.85Gi），超过 80%"
	it := Issue{
		Reason:  "NFSHighUsage",
		NodeIP:  "10.1.2.3",
		Message: msg,
	}
	if got := EnrichIssueMessage(it); got != msg {
		t.Fatalf("should not change: got %q", got)
	}
}

func TestIssueLocationLinesNFS(t *testing.T) {
	lines := IssueLocationLines(Issue{
		Reason: "NFSHighUsage",
		NodeIP: "192.168.0.5",
		Evidence: []string{
			"path=/data",
		},
	})
	if len(lines) != 1 || !containsAll(lines[0], "192.168.0.5", "/data") {
		t.Fatalf("lines=%v", lines)
	}
}

func TestEnrichNFSIssueMessageSCFallback(t *testing.T) {
	it := Issue{
		Reason:  "NFSHighUsage",
		NodeIP:  "10.9.8.7",
		Message: "NFS SC=nfs-client 使用率 96%（763.10Gi / 797.85Gi），超过 80%",
	}
	got := EnrichIssueMessage(it)
	if !containsAll(got, "10.9.8.7", "使用率 96%") || strings.Contains(got, "SC=nfs-client") {
		t.Fatalf("got %q", got)
	}
}

func TestEnrichLargeEphemeralIncludesNode(t *testing.T) {
	it := Issue{
		Category: "storage",
		Kind:     "Pod",
		Reason:   "LargeEphemeral",
		Node:     "worker-1",
		Message:  "非持久化存储占用 6.2Gi，超过 5Gi",
	}
	got := EnrichIssueMessage(it)
	if !containsAll(got, "节点", "worker-1") {
		t.Fatalf("got %q", got)
	}
}

func TestFinalizeIssueMessages(t *testing.T) {
	issues := []Issue{{
		Reason:  "NFSHighUsage",
		NodeIP:  "10.1.2.3",
		Evidence: []string{"path=/export"},
		Message: "NFS 使用率 96%（763.10Gi / 797.85Gi），超过 80%",
	}}
	FinalizeIssueMessages(issues)
	if !containsAll(issues[0].Message, "10.1.2.3:/export", "使用率 96%") {
		t.Fatalf("got %q", issues[0].Message)
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
