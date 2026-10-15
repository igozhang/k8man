package inspect

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

func TestRecoveryInterval(t *testing.T) {
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	if recoveryInterval(start, start.Add(5*time.Hour)) != recoveryFastEvery {
		t.Fatal("inside 6h should stay on the fast cadence")
	}
	if recoveryInterval(start, start.Add(recoveryFastFor)) != recoverySlowEvery {
		t.Fatal("at 6h should slow down")
	}
}

func TestNoteAlertFiredAndReschedule(t *testing.T) {
	w := New(nil, nil, nil)
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	w.noteAlertFired("c1")
	w.recoveryMu.Lock()
	st := w.recovery["c1"]
	w.recoveryMu.Unlock()
	if st == nil || !st.nextAt.Equal(now.Add(recoveryFirstDelay)) || st.generation != 1 {
		t.Fatalf("first schedule %+v", st)
	}
	gen := st.generation
	now = now.Add(recoveryFastFor + time.Minute)
	w.now = func() time.Time { return now }
	w.rescheduleRecovery("c1", gen)
	w.recoveryMu.Lock()
	st = w.recovery["c1"]
	w.recoveryMu.Unlock()
	if st == nil || !st.nextAt.Equal(now.Add(recoverySlowEvery)) {
		t.Fatalf("slow schedule %+v", st)
	}
	firedAt := now
	w.noteAlertFired("c1")
	w.recoveryMu.Lock()
	st = w.recovery["c1"]
	w.recoveryMu.Unlock()
	if st.generation == gen || !st.startedAt.Equal(firedAt) || !st.nextAt.Equal(firedAt.Add(recoveryFirstDelay)) {
		t.Fatalf("new alert should restart the fast window: %+v", st)
	}
	w.finishRecoveryProbe("c1", gen)
	w.recoveryMu.Lock()
	if _, ok := w.recovery["c1"]; !ok {
		t.Fatal("stale generation must not drop the new schedule")
	}
	w.recoveryMu.Unlock()
}

func TestScheduleOpenAlertsNeedsWebhook(t *testing.T) {
	st := openRecoveryStore(t)
	defer st.Close()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if err := st.UpsertAlertSent("c1", "pod|Pod|ns|web|CrashLoopBackOff", "critical", "detail", now); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	w.now = func() time.Time { return now }
	w.scheduleOpenAlerts()
	if len(w.recovery) != 0 {
		t.Fatal("no webhook should not arm a probe")
	}
	if err := st.CreateAlertWebhook(&model.AlertWebhook{
		ID: "h1", Name: "t", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w.scheduleOpenAlerts()
	stTrack := w.recovery["c1"]
	if stTrack == nil || !stTrack.nextAt.Equal(now.Add(recoveryFirstDelay)) {
		t.Fatalf("startup schedule %+v", stTrack)
	}
}

func TestKickRecoveryProbeStopsWhenCleared(t *testing.T) {
	st := openRecoveryStore(t)
	defer st.Close()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if err := st.CreateAlertWebhook(&model.AlertWebhook{
		ID: "h1", Name: "t", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAlertSent("c1", "pod|Pod|ns|web|CrashLoopBackOff", "critical", "detail", now); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	w.now = func() time.Time { return now }
	w.noteAlertFired("c1")
	called := 0
	w.runProbe = func(context.Context, string, bool, bool) (*Detail, error) {
		called++
		if err := st.DeleteAlertSent("c1", []string{"pod|Pod|ns|web|CrashLoopBackOff"}); err != nil {
			t.Errorf("delete sent: %v", err)
		}
		return nil, nil
	}
	w.kickRecoveryProbes(context.Background())
	if called != 0 {
		t.Fatalf("probe ran before the first delay, called=%d", called)
	}
	now = now.Add(recoveryFirstDelay)
	w.now = func() time.Time { return now }
	w.kickRecoveryProbes(context.Background())
	waitIdle(t, w)
	if called != 1 {
		t.Fatalf("called=%d", called)
	}
	w.recoveryMu.Lock()
	defer w.recoveryMu.Unlock()
	if _, ok := w.recovery["c1"]; ok {
		t.Fatal("cleared alert should stop the probe")
	}
}

func TestKickArmsOpenAlerts(t *testing.T) {
	st := openRecoveryStore(t)
	defer st.Close()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if err := st.CreateAlertWebhook(&model.AlertWebhook{
		ID: "h1", Name: "t", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAlertSent("c1", "pod|Pod|ns|web|CrashLoopBackOff", "critical", "detail", now); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	w.now = func() time.Time { return now }
	called := 0
	w.runProbe = func(context.Context, string, bool, bool) (*Detail, error) {
		called++
		return nil, nil
	}
	w.clearRecovery()
	w.kickRecoveryProbes(context.Background())
	if called != 0 {
		t.Fatalf("arming should wait 3 minutes, called=%d", called)
	}
	w.recoveryMu.Lock()
	stTrack := w.recovery["c1"]
	w.recoveryMu.Unlock()
	if stTrack == nil || !stTrack.nextAt.Equal(now.Add(recoveryFirstDelay)) {
		t.Fatalf("open alert should be armed %+v", stTrack)
	}
}

func TestRecoveryProbeDropsDeletedCluster(t *testing.T) {
	st := openRecoveryStore(t)
	defer st.Close()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if err := st.CreateAlertWebhook(&model.AlertWebhook{
		ID: "h1", Name: "t", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	fp := "kubeconfig|Kubeconfig||prod|Unreachable"
	if err := st.UpsertAlertSent("gone", fp, "critical", "detail", now); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	w.now = func() time.Time { return now }
	w.noteAlertFired("gone")
	now = now.Add(recoveryFirstDelay)
	w.now = func() time.Time { return now }
	w.runProbe = func(context.Context, string, bool, bool) (*Detail, error) {
		return nil, ErrNotFound
	}
	w.kickRecoveryProbes(context.Background())
	waitIdle(t, w)
	w.recoveryMu.Lock()
	_, ok := w.recovery["gone"]
	w.recoveryMu.Unlock()
	if ok {
		t.Fatal("deleted cluster should leave the probe schedule")
	}
	left, err := st.ListAlertSent("gone")
	if err != nil || len(left) != 0 {
		t.Fatalf("sent should be cleared: %v %+v", err, left)
	}
}

func openRecoveryStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func waitIdle(t *testing.T, w *Worker) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.inFlight.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recovery probe did not finish")
}
