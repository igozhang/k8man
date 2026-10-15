package inspect

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/store"
)

func TestAggregateDayMergesLeftoverRounds(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root)
	loc := beijingLoc
	day := time.Date(2026, 9, 20, 0, 0, 0, 0, loc)

	row1 := &store.InspectionRow{
		ClusterID: "c1", CheckedAt: time.Date(2026, 9, 20, 10, 0, 0, 0, loc),
		ProbeStatus: "ok", Warning: 1, Summary: "1 警告",
	}
	issues1 := []kube.Issue{{
		Severity: "warning", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "p1",
		Reason: "CrashLoopBackOff", Message: "boom",
	}}
	if err := fs.DumpRound("基地1", row1, issues1, loc); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.AggregateDay(day, loc); err != nil {
		t.Fatal(err)
	}

	// Leftover round after day.jsonl already exists must merge, not overwrite.
	row2 := &store.InspectionRow{
		ClusterID: "c1", CheckedAt: time.Date(2026, 9, 20, 16, 0, 0, 0, loc),
		ProbeStatus: "ok", Critical: 1, Summary: "1 严重",
	}
	issues2 := []kube.Issue{{
		Severity: "critical", Category: "node", Kind: "Node", Name: "n1",
		Reason: "NotReady", Message: "down",
	}}
	if err := fs.DumpRound("基地1", row2, issues2, loc); err != nil {
		t.Fatal(err)
	}
	aggs, err := fs.AggregateDay(day, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggs) != 1 {
		t.Fatalf("clusters %d", len(aggs))
	}
	if aggs[0].Rounds < 2 {
		t.Fatalf("rounds=%d want >=2", aggs[0].Rounds)
	}
	if len(aggs[0].Issues) != 2 {
		t.Fatalf("issues=%d want 2 (merged)", len(aggs[0].Issues))
	}
	loaded, err := fs.LoadDay(day, loc)
	if err != nil || len(loaded) != 1 || len(loaded[0].Issues) != 2 {
		t.Fatalf("persisted merge: err=%v issues=%v", err, loaded)
	}
}

func TestDumpAggregateDayAndRange(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root)
	loc := beijingLoc
	day := time.Date(2026, 9, 20, 0, 0, 0, 0, loc)

	row1 := &store.InspectionRow{
		ClusterID: "c1", CheckedAt: time.Date(2026, 9, 20, 10, 0, 0, 0, loc),
		ProbeStatus: "ok", Warning: 1, Summary: "1 警告",
	}
	issues1 := []kube.Issue{{
		Severity: "warning", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "p1",
		Reason: "CrashLoopBackOff", Message: "boom", SinceAt: "2026-09-18T00:00:00Z",
	}}
	if err := fs.DumpRound("基地1", row1, issues1, loc); err != nil {
		t.Fatal(err)
	}
	row2 := &store.InspectionRow{
		ClusterID: "c1", CheckedAt: time.Date(2026, 9, 20, 14, 0, 0, 0, loc),
		ProbeStatus: "ok", Warning: 1, Critical: 1, Summary: "1 严重 / 1 警告",
	}
	issues2 := []kube.Issue{
		{
			Severity: "warning", Category: "pod", Kind: "Pod", Namespace: "ns", Name: "p1",
			Reason: "CrashLoopBackOff", Message: "still", SinceAt: "2026-09-18T00:00:00Z",
		},
		{
			Severity: "critical", Category: "node", Kind: "Node", Name: "n1",
			Reason: "NotReady", Message: "down",
		},
	}
	if err := fs.DumpRound("基地1", row2, issues2, loc); err != nil {
		t.Fatal(err)
	}

	aggs, err := fs.AggregateDay(day, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggs) != 1 {
		t.Fatalf("clusters %d", len(aggs))
	}
	if aggs[0].Rounds != 2 || len(aggs[0].Issues) != 2 {
		t.Fatalf("rounds=%d issues=%d", aggs[0].Rounds, len(aggs[0].Issues))
	}
	// round dirs removed
	rounds, _ := fs.loadAllRounds(day, loc)
	if len(rounds) != 0 {
		t.Fatal("rounds should be cleaned")
	}
	loaded, err := fs.LoadDay(day, loc)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load day: %v %d", err, len(loaded))
	}

	// second day with same fingerprint → SeenDays=2 in range
	day2 := day.AddDate(0, 0, 1)
	row3 := &store.InspectionRow{
		ClusterID: "c1", CheckedAt: time.Date(2026, 9, 21, 11, 0, 0, 0, loc),
		ProbeStatus: "ok", Warning: 1, Summary: "1 警告",
	}
	if err := fs.DumpRound("基地1", row3, issues1, loc); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.AggregateDay(day2, loc); err != nil {
		t.Fatal(err)
	}
	rangeAggs, err := fs.AggregateRange(day, day2, loc, time.Date(2026, 9, 22, 12, 0, 0, 0, loc))
	if err != nil {
		t.Fatal(err)
	}
	if len(rangeAggs) != 1 {
		t.Fatalf("range clusters %d", len(rangeAggs))
	}
	var crashDays int
	for _, it := range rangeAggs[0].Issues {
		if it.Reason == "CrashLoopBackOff" {
			crashDays = it.SeenDays
		}
	}
	if crashDays != 2 {
		t.Fatalf("seenDays=%d want 2", crashDays)
	}
	_ = filepath.Join(root, InspectionDirName)
}

func TestPruneOlderThan(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root)
	loc := beijingLoc
	old := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)
	row := &store.InspectionRow{ClusterID: "c1", CheckedAt: old.Add(12 * time.Hour), ProbeStatus: "ok"}
	if err := fs.DumpRound("x", row, nil, loc); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.AggregateDay(old, loc); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, loc)
	if err := fs.PruneOlderThan(60, now, loc); err != nil {
		t.Fatal(err)
	}
	got, _ := fs.LoadDay(old, loc)
	if len(got) != 0 {
		t.Fatal("old day should be pruned")
	}
}

func TestPruneRetentionKeepsNewestDays(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root)
	loc := beijingLoc
	days := []time.Time{
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc),
		time.Date(2026, 9, 2, 0, 0, 0, 0, loc),
		time.Date(2026, 9, 3, 0, 0, 0, 0, loc),
	}
	for _, day := range days {
		row := &store.InspectionRow{ClusterID: "c1", CheckedAt: day.Add(12 * time.Hour), ProbeStatus: "ok"}
		if err := fs.DumpRound("x", row, nil, loc); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, loc)
	if err := fs.PruneRetention(0, 2, now, loc); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fs.dayDir(days[0], loc)); !os.IsNotExist(err) {
		t.Fatal("oldest day should be removed")
	}
	for _, day := range days[1:] {
		if st, err := os.Stat(fs.dayDir(day, loc)); err != nil || !st.IsDir() {
			t.Fatalf("day %s should stay: %v", day.Format("2006-01-02"), err)
		}
	}
}
