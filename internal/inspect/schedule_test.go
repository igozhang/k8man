package inspect

import (
	"testing"
	"time"

	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

func TestShouldSkip(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	if ShouldSkip(now, time.Time{}) {
		t.Fatal("zero checkedAt must not skip")
	}
	if !ShouldSkip(now, now.Add(-30*time.Minute)) {
		t.Fatal("30m ago should skip")
	}
	if ShouldSkip(now, now.Add(-MinAge-time.Minute)) {
		t.Fatal("older than MinAge must not skip")
	}
}

func TestComputeNextDue(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	got := ComputeNextDue(now, 0)
	if !got.Equal(now.Add(Interval)) {
		t.Fatalf("zero jitter: got %s", got)
	}
	got = ComputeNextDue(now, Jitter)
	if !got.Equal(now.Add(Interval + Jitter)) {
		t.Fatalf("+jitter: got %s", got)
	}
	got = ComputeNextDue(now, 24*time.Hour)
	if !got.Equal(now.Add(Interval + Jitter)) {
		t.Fatalf("jitter should clamp: got %s", got)
	}
}

func TestPickDueStaggerAndSkip(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	a := model.Cluster{ID: "aaa", Name: "a"}
	b := model.Cluster{ID: "bbb", Name: "b"}
	clusters := []model.Cluster{a, b}

	if id := PickDue(now, clusters, map[string]store.InspectionRow{}); id == "" {
		t.Fatal("never inspected should be due")
	}
	first := PickDue(now, clusters, nil)
	second := PickDue(now, clusters, nil)
	if first != second {
		t.Fatalf("stable order: %s vs %s", first, second)
	}
	other := "aaa"
	if first == "aaa" {
		other = "bbb"
	}
	if Hash32(first) > Hash32(other) {
		t.Fatalf("expected lower hash first: %s hash=%d %s hash=%d", first, Hash32(first), other, Hash32(other))
	}

	rows := map[string]store.InspectionRow{
		"aaa": {ClusterID: "aaa", CheckedAt: now.Add(-30 * time.Minute), NextDue: now.Add(-time.Minute)},
		"bbb": {ClusterID: "bbb", CheckedAt: now.Add(-3 * time.Hour), NextDue: now.Add(-time.Minute)},
	}
	if got := PickDue(now, clusters, rows); got != "bbb" {
		t.Fatalf("recent aaa should skip, want bbb got %s", got)
	}

	rows["bbb"] = store.InspectionRow{ClusterID: "bbb", CheckedAt: now.Add(-3 * time.Hour), NextDue: now.Add(time.Hour)}
	if got := PickDue(now, clusters, rows); got != "" {
		t.Fatalf("nothing due, got %s", got)
	}
}

func TestHash32Stable(t *testing.T) {
	if Hash32("x") != Hash32("x") || Hash32("x") == Hash32("y") {
		t.Fatal("hash should be stable and differ by id")
	}
}
