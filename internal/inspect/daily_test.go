package inspect

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

func TestPreviousCalendarDayAndGate(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	// 2026-09-21 01:05 CST → report day 2026-09-20
	now := time.Date(2026, 9, 21, 1, 5, 0, 0, loc)
	from, to, day := previousCalendarDay(now, loc)
	if !day.Equal(time.Date(2026, 9, 20, 0, 0, 0, 0, loc)) {
		t.Fatalf("day %v", day)
	}
	if !from.Equal(day) {
		t.Fatalf("from %v", from)
	}
	if !to.Before(time.Date(2026, 9, 21, 0, 0, 0, 0, loc)) {
		t.Fatalf("to %v", to)
	}
	if dailyArchiveOpen(time.Date(2026, 9, 21, 0, 59, 0, 0, loc), loc) {
		t.Fatal("before 01:00 should be closed")
	}
	if !dailyArchiveOpen(now, loc) {
		t.Fatal("01:05 should be open")
	}
	next := nextDailyArchiveAt(time.Date(2026, 9, 21, 0, 30, 0, 0, loc), loc, 6*time.Minute)
	want := time.Date(2026, 9, 21, 1, 6, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next %v want %v", next, want)
	}
	next = nextDailyArchiveAt(time.Date(2026, 9, 21, 2, 0, 0, 0, loc), loc, 0)
	want = time.Date(2026, 9, 22, 1, 0, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next day %v want %v", next, want)
	}
}

func TestArchivePreviousDayIfDue(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w := New(st, nil, nil)
	loc := time.FixedZone("CST", 8*3600)
	// Force beijing clock via request location inside archive uses beijingLoc.
	// Use a time that is 01:10 in Asia/Shanghai.
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		sh = loc
	}
	now := time.Date(2026, 9, 21, 1, 10, 0, 0, sh)

	if err := w.archivePreviousDayIfDue(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if docs, _ := st.ListDocs(DocCategoryInspectionReport, ""); len(docs) != 0 {
		t.Fatalf("should skip when clusters <= 2, got %d", len(docs))
	}

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"c1", "c2", "c3"} {
		cl := &model.Cluster{
			ID: id, Name: "基地" + id, Kubeconfig: "k", PermType: "cluster",
			Namespaces: []string{}, Status: "ok", CreatedAt: base, UpdatedAt: base,
		}
		if err := st.CreateCluster(cl); err != nil {
			t.Fatal(err)
		}
		checked := time.Date(2026, 9, 20, 10, 0, 0, 0, sh)
		row := &store.InspectionRow{
			ClusterID: id, CheckedAt: checked, NextDue: checked.Add(2 * time.Hour),
			ProbeStatus: "ok", Warning: i, Summary: "ok",
			ReportJSON: `{"issues":[{"severity":"warning","category":"pod","name":"p","reason":"OldStillOpen","message":"still-open","sinceAt":"2026-08-01T00:00:00Z"}]}`,
		}
		if err := st.UpsertInspection(row); err != nil {
			t.Fatal(err)
		}
	}
	// Still before 01:00 Shanghai on the 21st → skip
	early := time.Date(2026, 9, 21, 0, 40, 0, 0, sh)
	if err := w.archivePreviousDayIfDue(context.Background(), early); err != nil {
		t.Fatal(err)
	}
	if docs, _ := st.ListDocs("", ""); len(docs) != 0 {
		t.Fatalf("before 01:00 created %d", len(docs))
	}

	if err := w.archivePreviousDayIfDue(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	docs, err := st.ListDocs(DocCategoryInspectionReport, "")
	if err != nil || len(docs) != 1 {
		t.Fatalf("docs %v %d", err, len(docs))
	}
	full, err := st.GetDoc(docs[0].ID)
	if err != nil || full == nil {
		t.Fatal(err)
	}
	if full.Slug != "inspectall20260920" || full.ID != "inspectall20260920" {
		t.Fatalf("id=%s slug=%s", full.ID, full.Slug)
	}
	if !strings.Contains(full.Title, "2026-09-20") || !strings.Contains(full.Content, "自动归档") {
		t.Fatalf("title=%s", full.Title)
	}
	if !strings.Contains(full.Content, "基地c1") || !strings.Contains(full.Content, "OldStillOpen") {
		t.Fatalf("missing cluster section or long-running issue")
	}
	if err := w.archivePreviousDayIfDue(context.Background(), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	docs, _ = st.ListDocs(DocCategoryInspectionReport, "")
	if len(docs) != 1 {
		t.Fatalf("duplicate archive %d", len(docs))
	}
}

func TestRepairAllDayDocID(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	d := &model.Doc{
		ID: "f24f13b0-5345-495f-a6de-861437fb485b", Title: "巡检报告 · 日 · 所有基地 · 2026-09-20",
		Category: DocCategoryInspectionReport, Tags: []string{"巡检报告"}, Summary: "s", Content: "body",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateDoc(d); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	if err := w.RepairAllDayDocIDs(); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDoc("inspectall20260920")
	if err != nil || got == nil {
		t.Fatalf("rekey: %v %+v", err, got)
	}
	if got.Slug != "inspectall20260920" || got.Content != "body" {
		t.Fatalf("%+v", got)
	}
	old, _ := st.GetDoc(d.ID)
	if old != nil {
		t.Fatal("uuid id should be gone")
	}
}

func TestPruneAutoDailyDocs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, sh)
	mk := func(id, slug string, pinned bool, created time.Time) {
		t.Helper()
		d := &model.Doc{
			ID: id, Title: id, Slug: slug, Category: DocCategoryInspectionReport,
			Tags: []string{}, Summary: "s", Content: "body", Pinned: pinned,
			CreatedAt: created, UpdatedAt: created,
		}
		if err := st.CreateDoc(d); err != nil {
			t.Fatal(err)
		}
	}
	mk("inspectall20251014", "inspectall20251014", false, now.AddDate(0, 0, -366))
	mk("inspectall20251015", "inspectall20251015", false, now)
	mk("inspectall20200101", "inspectall20200101", true, now.AddDate(-6, 0, 0))
	mk("manual-week", "manual-week", false, now.AddDate(-2, 0, 0))

	w := New(st, nil, nil)
	w.retentionDays = 365
	w.retentionMax = 365
	if err := w.pruneRetention(now); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetDoc("inspectall20251014"); d != nil {
		t.Fatal("day older than 365 should be removed")
	}
	for _, id := range []string{"inspectall20251015", "inspectall20200101", "manual-week"} {
		if d, _ := st.GetDoc(id); d == nil {
			t.Fatalf("%s should stay", id)
		}
	}

	mk("inspectall20261001", "inspectall20261001", false, now)
	mk("inspectall20261002", "inspectall20261002", false, now)
	mk("inspectall20261003", "inspectall20261003", false, now)
	w.retentionMax = 2
	if err := w.pruneRetention(now); err != nil {
		t.Fatal(err)
	}
	left, err := st.ListDocs(DocCategoryInspectionReport, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, d := range left {
		ids[d.ID] = true
	}
	if ids["inspectall20251015"] || ids["inspectall20261001"] || !ids["inspectall20261002"] || !ids["inspectall20261003"] {
		t.Fatalf("count cap ids=%v", ids)
	}
	if !ids["inspectall20200101"] || !ids["manual-week"] {
		t.Fatalf("pinned and manual should stay: %v", ids)
	}
}
