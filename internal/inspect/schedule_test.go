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

func TestEffectiveConcurrency(t *testing.T) {
	cases := []struct {
		due, base, max, want int
	}{
		{0, 4, 8, 4},
		{3, 4, 8, 4},
		{4, 4, 8, 4},
		{5, 4, 8, 5},
		{8, 4, 8, 8},
		{200, 4, 8, 8},
		{10, 0, 0, 8}, // falls back to defaults then clamps
		{3, 4, 2, 4},   // max < base → max raised to base in EffectiveConcurrency? 
	}
	// For max < base, EffectiveConcurrency sets max = base, so due=3 → 4
	if got := EffectiveConcurrency(3, 4, 2); got != 4 {
		t.Fatalf("max<base: got %d want 4", got)
	}
	for _, c := range cases[:6] {
		if got := EffectiveConcurrency(c.due, c.base, c.max); got != c.want {
			t.Fatalf("due=%d base=%d max=%d: got %d want %d", c.due, c.base, c.max, got, c.want)
		}
	}
	if got := EffectiveConcurrency(10, 0, 0); got != DefaultConcurrencyMax {
		// base defaults to 4, due=10 > 4, max defaults to 8 → 8
		t.Fatalf("defaults: got %d want %d", got, DefaultConcurrencyMax)
	}
}

func TestListDueMultiple(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	clusters := []model.Cluster{{ID: "aaa"}, {ID: "bbb"}, {ID: "ccc"}}
	ids := ListDue(now, clusters, nil)
	if len(ids) != 3 {
		t.Fatalf("want 3 due, got %v", ids)
	}
	if PickDue(now, clusters, nil) != ids[0] {
		t.Fatalf("PickDue should match ListDue[0]")
	}
}
