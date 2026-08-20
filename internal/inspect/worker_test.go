package inspect

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

func TestRunClusterCanceledDoesNotPersist(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	if err := st.CreateCluster(&model.Cluster{
		ID: "c1", Name: "prod", Kubeconfig: "invalid", PermType: "unknown",
		Namespaces: []string{}, Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.RunCluster(ctx, "c1", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	got, err := st.GetInspection("c1")
	if err != nil || got != nil {
		t.Fatalf("should not persist: %v %+v", err, got)
	}
	cl, err := st.GetCluster("c1", false)
	if err != nil || cl == nil || cl.Status != "unknown" {
		t.Fatalf("cluster status should stay unknown: %v %+v", err, cl)
	}
}
