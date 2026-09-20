package inspect

import (
	"strings"
	"testing"
	"time"

	"gitee.com/k8man/internal/kube"
)

func TestPeriodWindow(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	// 2026-09-20 Sunday 17:00 CST
	now := time.Date(2026, 9, 20, 17, 0, 0, 0, loc)

	from, to := PeriodWindow(ExportPeriodDay, now, loc)
	if !from.Equal(time.Date(2026, 9, 20, 0, 0, 0, 0, loc)) {
		t.Fatalf("day from = %v", from)
	}
	if !to.Equal(now) {
		t.Fatalf("day to = %v", to)
	}

	from, _ = PeriodWindow(ExportPeriodWeek, now, loc)
	// Monday 2026-09-14
	if !from.Equal(time.Date(2026, 9, 14, 0, 0, 0, 0, loc)) {
		t.Fatalf("week from = %v", from)
	}

	from, _ = PeriodWindow(ExportPeriodMonth, now, loc)
	if !from.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("month from = %v", from)
	}
}

func TestIssueInWindow(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	from := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	to := time.Date(2026, 9, 20, 17, 0, 0, 0, loc)

	keepEmpty := kube.Issue{Name: "a"}
	if !issueInWindow(keepEmpty, from, to) {
		t.Fatal("empty sinceAt should keep")
	}

	inside := kube.Issue{SinceAt: time.Date(2026, 9, 15, 12, 0, 0, 0, loc).UTC().Format(time.RFC3339)}
	if !issueInWindow(inside, from, to) {
		t.Fatal("inside should keep")
	}

	outside := kube.Issue{SinceAt: time.Date(2026, 9, 1, 0, 0, 0, 0, loc).UTC().Format(time.RFC3339)}
	if issueInWindow(outside, from, to) {
		t.Fatal("outside should drop")
	}
}

func TestParseExportDefaults(t *testing.T) {
	if ParseExportPeriod("") != ExportPeriodWeek {
		t.Fatal("default period")
	}
	if ParseExportScope("") != ExportScopeAll {
		t.Fatal("default scope")
	}
	if ParseExportPeriod("day") != ExportPeriodDay {
		t.Fatal("day")
	}
	if ParseExportPeriod("m") != ExportPeriodWeek {
		t.Fatal("bare m should not mean month")
	}
	if ParseExportPeriod("month") != ExportPeriodMonth {
		t.Fatal("month")
	}
	if ParseExportScope("current") != ExportScopeCurrent {
		t.Fatal("current")
	}
}

func TestEscapeMD(t *testing.T) {
	got := escapeMD("a|b<script>`x`\nok")
	if strings.Contains(got, "<script>") || strings.Contains(got, "`") || strings.Contains(got, "\n") {
		t.Fatalf("escapeMD = %q", got)
	}
	if !strings.Contains(got, "\\|") {
		t.Fatalf("pipe not escaped: %q", got)
	}
}
