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
	if _, err := w.Get("c1"); err != ErrNotFound {
		t.Fatalf("get never inspected: %v", err)
	}
}
