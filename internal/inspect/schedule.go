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
	Interval    = 2 * time.Hour
	Jitter      = 6 * time.Minute
	MinAge      = 110 * time.Minute
	MinGap      = 15 * time.Second
	Tick        = time.Minute
	RunTimeout  = 45 * time.Second
	PreviewMax  = 8
	PreviewClip = 120
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

type dueCandidate struct {
	id   string
	due  time.Time
	hash uint32
	seen bool
}

// PickDue returns the next cluster that should run. Never-inspected clusters
// come first (stable hash order); otherwise the earliest nextDue that is due
// and not inside MinAge. Empty string means nothing is due.
func PickDue(now time.Time, clusters []model.Cluster, rows map[string]store.InspectionRow) string {
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
	if len(cands) == 0 {
		return ""
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
	return cands[0].id
}

func inspectionMap(rows []store.InspectionRow) map[string]store.InspectionRow {
	out := make(map[string]store.InspectionRow, len(rows))
	for _, r := range rows {
		out[r.ClusterID] = r
	}
	return out
}
