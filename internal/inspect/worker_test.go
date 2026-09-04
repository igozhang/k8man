package inspect

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
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

func TestClusterGateSerializesSameID(t *testing.T) {
	w := New(nil, nil, nil)
	var concurrent atomic.Int32
	var maxSeen atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.acquireCluster(context.Background(), "same"); err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			n := concurrent.Add(1)
			for {
				cur := maxSeen.Load()
				if n <= cur || maxSeen.CompareAndSwap(cur, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			concurrent.Add(-1)
			w.releaseCluster("same")
		}()
	}
	wg.Wait()
	if maxSeen.Load() != 1 {
		t.Fatalf("same cluster must be exclusive, max concurrent=%d", maxSeen.Load())
	}
}

func TestTryAcquireBusy(t *testing.T) {
	w := New(nil, nil, nil)
	if !w.tryAcquireCluster("c1") {
		t.Fatal("first acquire should succeed")
	}
	if w.tryAcquireCluster("c1") {
		t.Fatal("second try should fail while held")
	}
	if !w.tryAcquireCluster("c2") {
		t.Fatal("other cluster should succeed")
	}
	w.releaseCluster("c1")
	w.releaseCluster("c2")
	if !w.tryAcquireCluster("c1") {
		t.Fatal("after release should succeed")
	}
	w.releaseCluster("c1")
}
