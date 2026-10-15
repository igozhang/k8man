package inspect

import (
	"context"
	"errors"
	"log"
	"sort"
	"time"
)

const (
	recoveryFirstDelay = 3 * time.Minute
	recoveryFastEvery  = 5 * time.Minute
	recoverySlowEvery  = 15 * time.Minute
	recoveryFastFor    = 6 * time.Hour
	recoveryTick       = 30 * time.Second
)

// recoveryTrack is one cluster with an open external alert.
// generation bumps when a new alert is delivered so an in-flight probe
// does not overwrite the fresh 3-minute delay.
type recoveryTrack struct {
	startedAt  time.Time
	nextAt     time.Time
	generation int
}

func recoveryInterval(started, now time.Time) time.Duration {
	if !started.IsZero() && now.Sub(started) >= recoveryFastFor {
		return recoverySlowEvery
	}
	return recoveryFastEvery
}

func (w *Worker) recoveryProbeLoop(ctx context.Context) {
	if w == nil {
		return
	}
	w.scheduleOpenAlerts()
	for {
		if ctx.Err() != nil {
			return
		}
		w.kickRecoveryProbes(ctx)
		if sleepCtx(ctx, recoveryTick) != nil {
			return
		}
	}
}

// noteAlertFired schedules a recovery probe 3 minutes after an external alert is delivered.
// A newer alert restarts the 6-hour fast window.
func (w *Worker) noteAlertFired(clusterID string) {
	if w == nil || clusterID == "" {
		return
	}
	now := w.now()
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	if w.recovery == nil {
		w.recovery = map[string]*recoveryTrack{}
	}
	gen := 1
	if prev := w.recovery[clusterID]; prev != nil {
		gen = prev.generation + 1
	}
	w.recovery[clusterID] = &recoveryTrack{
		startedAt:  now,
		nextAt:     now.Add(recoveryFirstDelay),
		generation: gen,
	}
}

// scheduleOpenAlerts arms probes for alerts that were already open at startup.
// The first wait is 3 minutes, then the 5-minute cadence; the 6-hour window starts now.
func (w *Worker) scheduleOpenAlerts() {
	if w == nil || w.Store == nil {
		return
	}
	if ok, err := w.alertsEnabled(); err != nil || !ok {
		return
	}
	w.armMissingAlerts()
}

// armMissingAlerts schedules clusters that still have a delivered alert and are not already tracked.
func (w *Worker) armMissingAlerts() {
	if w == nil || w.Store == nil {
		return
	}
	ids, err := w.Store.ListAlertedClusterIDs()
	if err != nil {
		log.Printf("recovery probe list: %v", err)
		return
	}
	now := w.now()
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	if w.recovery == nil {
		w.recovery = map[string]*recoveryTrack{}
	}
	for _, id := range ids {
		if _, exists := w.recovery[id]; exists {
			continue
		}
		w.recovery[id] = &recoveryTrack{
			startedAt:  now,
			nextAt:     now.Add(recoveryFirstDelay),
			generation: 1,
		}
	}
}

// DropRecovery stops probing a cluster. In-flight probes still finish, but will not be rescheduled
// unless a newer alert is delivered.
func (w *Worker) DropRecovery(id string) {
	if w == nil || id == "" {
		return
	}
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	delete(w.recovery, id)
}

func (w *Worker) alertsEnabled() (bool, error) {
	if w == nil || w.Store == nil {
		return false, nil
	}
	hooks, err := w.Store.ListEnabledAlertWebhooks()
	if err != nil {
		return false, err
	}
	return len(hooks) > 0, nil
}

func (w *Worker) kickRecoveryProbes(ctx context.Context) {
	if w == nil || w.Store == nil {
		return
	}
	enabled, err := w.alertsEnabled()
	if err != nil {
		log.Printf("recovery probe hooks: %v", err)
		return
	}
	if !enabled {
		w.clearRecovery()
		return
	}
	// Pick up alerts that were open before this process, or after webhooks were turned back on.
	// Existing tracks are left alone, so a fresh 3-minute delay is not reset every tick.
	w.armMissingAlerts()
	now := w.now()
	type dueProbe struct {
		id  string
		gen int
	}
	w.recoveryMu.Lock()
	var due []dueProbe
	for id, st := range w.recovery {
		if st == nil || st.nextAt.After(now) {
			continue
		}
		due = append(due, dueProbe{id: id, gen: st.generation})
	}
	w.recoveryMu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].id < due[j].id })

	for _, item := range due {
		if ctx.Err() != nil {
			return
		}
		if !w.recoveryStillDue(item.id, item.gen, w.now()) {
			continue
		}
		if !w.tryAcquireCluster(item.id) {
			continue
		}
		if !w.tryTakeSlot(w.maxConcurrency) {
			w.releaseCluster(item.id)
			return
		}
		if err := w.reserveStart(ctx); err != nil {
			w.inFlight.Add(-1)
			w.releaseCluster(item.id)
			return
		}
		if !w.recoveryStillDue(item.id, item.gen, w.now()) {
			w.inFlight.Add(-1)
			w.releaseCluster(item.id)
			continue
		}
		go w.execRecoveryProbe(ctx, item.id, item.gen)
	}
}

func (w *Worker) recoveryStillDue(id string, gen int, now time.Time) bool {
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	st := w.recovery[id]
	return st != nil && st.generation == gen && !st.nextAt.After(now)
}

func (w *Worker) execRecoveryProbe(ctx context.Context, id string, gen int) {
	defer w.releaseCluster(id)
	var detail *Detail
	var err error
	func() {
		defer w.inFlight.Add(-1)
		if w.runProbe != nil {
			detail, err = w.runProbe(ctx, id, true, true)
			return
		}
		// Diagnose holds a concurrency slot. The webhook wait does not.
		detail, err = w.runClusterBody(ctx, id, true, pushLater)
	}()
	if errors.Is(err, ErrNotFound) {
		w.clearClusterAlerts(id)
		w.dropRecoveryIfGen(id, gen)
		return
	}
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		log.Printf("recovery probe %s: %v", id, err)
	}
	if ctx.Err() != nil {
		return
	}
	if w.runProbe == nil && err == nil && detail != nil && w.Store != nil {
		cl, gerr := w.Store.GetCluster(id, false)
		if gerr != nil {
			log.Printf("recovery probe cluster %s: %v", id, gerr)
			w.finishRecoveryProbe(id, gen)
			return
		}
		if cl == nil {
			w.clearClusterAlerts(id)
			w.dropRecoveryIfGen(id, gen)
			return
		}
		w.deliverAlerts(cl, detail.Probe.Status, detail.Diagnose, detail.Issues, true)
	}
	w.finishRecoveryProbe(id, gen)
}

func (w *Worker) clearClusterAlerts(id string) {
	if w == nil || w.Store == nil || id == "" {
		return
	}
	if err := w.Store.ClearAlertSent(id); err != nil {
		log.Printf("recovery probe clear %s: %v", id, err)
	}
}

func (w *Worker) finishRecoveryProbe(id string, gen int) {
	enabled, err := w.alertsEnabled()
	if err != nil {
		log.Printf("recovery probe hooks: %v", err)
		w.rescheduleRecovery(id, gen)
		return
	}
	if !enabled {
		w.dropRecoveryIfGen(id, gen)
		return
	}
	sent, err := w.Store.ListAlertSent(id)
	if err != nil {
		log.Printf("recovery probe sent %s: %v", id, err)
		w.rescheduleRecovery(id, gen)
		return
	}
	if len(sent) == 0 {
		w.dropRecoveryIfGen(id, gen)
		return
	}
	w.rescheduleRecovery(id, gen)
}

func (w *Worker) rescheduleRecovery(id string, gen int) {
	now := w.now()
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	st := w.recovery[id]
	if st == nil || st.generation != gen {
		return
	}
	st.nextAt = now.Add(recoveryInterval(st.startedAt, now))
}

func (w *Worker) dropRecoveryIfGen(id string, gen int) {
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	st := w.recovery[id]
	if st == nil || st.generation != gen {
		return
	}
	delete(w.recovery, id)
}

func (w *Worker) clearRecovery() {
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	w.recovery = map[string]*recoveryTrack{}
}
