package inspect

import (
	"path/filepath"
	"testing"
	"time"

	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

func TestListIncludesNeverInspected(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	if err := st.CreateCluster(&model.Cluster{
		ID: "c1", Name: "prod", Kubeconfig: "x", PermType: "cluster",
		Namespaces: []string{}, Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	out, err := w.List()
	if err != nil {
		t.Fatal(err)
	}
	if out.IntervalSeconds != int64(Interval/time.Second) || len(out.Items) != 1 {
		t.Fatalf("list %+v", out)
	}
	if out.Items[0].ClusterID != "c1" || out.Items[0].Summary != "尚未巡检" || out.Items[0].CheckedAt != nil {
		t.Fatalf("item %+v", out.Items[0])
	}
	if out.Items[0].HasScan || out.Items[0].ScanPods != 0 {
		t.Fatalf("unexpected scan fields %+v", out.Items[0])
	}
	if _, err := w.Get("c1"); err != ErrNotFound {
		t.Fatalf("get never inspected: %v", err)
	}
}

func TestListItemExposesScanPods(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	if err := st.CreateCluster(&model.Cluster{
		ID: "c1", Name: "prod", Kubeconfig: "x", PermType: "cluster",
		Namespaces: []string{}, Status: "ok", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	raw := `{"probe":{"status":"ok"},"diagnose":{"counts":{"pods":2},"scan":{"pods":120},"truncated":false},"issues":[{"category":"pod","severity":"critical","kind":"Pod","namespace":"ns","name":"a","reason":"CrashLoopBackOff"},{"category":"pod","severity":"warning","kind":"Pod","namespace":"ns","name":"b","reason":"NotReady"}]}`
	if err := st.UpsertInspection(&store.InspectionRow{
		ClusterID: "c1", CheckedAt: now, NextDue: now.Add(time.Hour), ProbeStatus: "ok",
		Critical: 1, Warning: 1, Summary: "ok", ReportJSON: raw,
	}); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	out, err := w.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 {
		t.Fatalf("items %d", len(out.Items))
	}
	it := out.Items[0]
	if !it.HasScan || it.ScanPods != 120 || it.PodIssues != 2 {
		t.Fatalf("scan fields %+v", it)
	}
}
