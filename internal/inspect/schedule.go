package inspect

import (
	"hash/fnv"
	"math/rand/v2"
	"sort"
	"time"

	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

const (
	Interval = 2 * time.Hour
	Jitter   = 6 * time.Minute
	MinAge   = 110 * time.Minute
	// MinStartGap is the minimum pause between *starting* two patrols.
	// Replaces the old global MinGap (15s after each run), which forced full serialisation.
	MinStartGap = 2 * time.Second
	Tick        = time.Minute
	RunTimeout  = 45 * time.Second
	PreviewMax  = 8
	PreviewClip = 120

	DefaultConcurrency    = 4
	DefaultConcurrencyMax = 8
)

// ShouldSkip reports whether a cluster was inspected too recently.
func ShouldSkip(now, checkedAt time.Time) bool {
	if checkedAt.IsZero() {
		return false
	}
	return now.Sub(checkedAt) < MinAge
}

// ComputeNextDue returns the next patrol time: 2h plus jitter in [-6m, +6m].
func ComputeNextDue(now time.Time, jitter time.Duration) time.Time {
	if jitter < -Jitter {
		jitter = -Jitter
	}
	if jitter > Jitter {
		jitter = Jitter
	}
	return now.Add(Interval + jitter)
}

// RandomJitter is uniform in [-Jitter, +Jitter].
func RandomJitter() time.Duration {
	n := rand.Int64N(int64(2*Jitter) + 1)
	return time.Duration(n) - Jitter
}

// Hash32 is a stable per-id order key used to stagger first runs.
func Hash32(id string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return h.Sum32()
}

// EffectiveConcurrency returns how many patrols may run at once.
// Base slots when backlog is small; expands toward max when dueCount > base.
func EffectiveConcurrency(dueCount, base, max int) int {
	if base < 1 {
		base = DefaultConcurrency
	}
	if max < 1 {
		max = DefaultConcurrencyMax
	}
	if max < base {
		max = base
	}
	if dueCount <= base {
		return base
	}
	if dueCount >= max {
		return max
	}
	return dueCount
}

type dueCandidate struct {
	id   string
	due  time.Time
	hash uint32
	seen bool
}

func dueCandidates(now time.Time, clusters []model.Cluster, rows map[string]store.InspectionRow) []dueCandidate {
	cands := make([]dueCandidate, 0, len(clusters))
	for _, c := range clusters {
		if c.ID == "" {
			continue
		}
		row, ok := rows[c.ID]
		if !ok || row.CheckedAt.IsZero() {
			cands = append(cands, dueCandidate{id: c.ID, hash: Hash32(c.ID)})
			continue
		}
		if ShouldSkip(now, row.CheckedAt) {
			continue
		}
		if !row.NextDue.IsZero() && row.NextDue.After(now) {
			continue
		}
		cands = append(cands, dueCandidate{id: c.ID, due: row.NextDue, hash: Hash32(c.ID), seen: true})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].seen != cands[j].seen {
			return !cands[i].seen
		}
		if !cands[i].seen {
			return cands[i].hash < cands[j].hash
		}
		if !cands[i].due.Equal(cands[j].due) {
			return cands[i].due.Before(cands[j].due)
		}
		return cands[i].hash < cands[j].hash
	})
	return cands
}

// ListDue returns all clusters that should run, earliest / never-inspected first.
func ListDue(now time.Time, clusters []model.Cluster, rows map[string]store.InspectionRow) []string {
	cands := dueCandidates(now, clusters, rows)
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.id
	}
	return out
}

// PickDue returns the next cluster that should run, or empty if none is due.
func PickDue(now time.Time, clusters []model.Cluster, rows map[string]store.InspectionRow) string {
	ids := ListDue(now, clusters, rows)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func inspectionMap(rows []store.InspectionRow) map[string]store.InspectionRow {
	out := make(map[string]store.InspectionRow, len(rows))
	for _, r := range rows {
		out[r.ClusterID] = r
	}
	return out
}
