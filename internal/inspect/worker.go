package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitee.com/k8man/internal/alert"
	"gitee.com/k8man/internal/hostsfile"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

// Options tunes background patrol concurrency and file dumps.
type Options struct {
	Concurrency    int // base parallel slots (default 4)
	ConcurrencyMax int // hard cap when backlog expands (default 8)
	DataDir        string
	RetentionDays  int // day dirs and auto daily docs older than this are removed (default 365)
	RetentionMax   int // keep at most this many day dirs / auto daily docs (default 365)
}

// Worker runs per-cluster patrols on a 2h stagger and serves stored snapshots.
type Worker struct {
	Store  *store.Store
	Pool   *kube.Pool
	Hosts  *hostsfile.File
	Alerts *alert.Notifier
	Files  *FileStore

	baseConcurrency int
	maxConcurrency  int
	retentionDays   int
	retentionMax    int

	now    func() time.Time
	jitter func() time.Duration

	gates     sync.Map // clusterID -> chan struct{} (cap 1)
	inFlight  atomic.Int32
	slotMu    sync.Mutex
	startMu   sync.Mutex
	lastStart time.Time
	dailyMu   sync.Mutex

	recoveryMu sync.Mutex
	recovery   map[string]*recoveryTrack
	alertRound sync.Map // clusterID -> int, round captured for the in-flight inspect

	reachMu sync.Mutex
	reach   map[string]*reachStreak // Probe Unreachable fire/recover hysteresis

	// runProbe, when set, replaces the scoped recovery body (tests).
	runProbe func(ctx context.Context, clusterID string, force, syncNotify bool) (*Detail, error)
}

func New(st *store.Store, pool *kube.Pool, hosts *hostsfile.File) *Worker {
	return NewWithOptions(st, pool, hosts, Options{})
}

func NewWithOptions(st *store.Store, pool *kube.Pool, hosts *hostsfile.File, opts Options) *Worker {
	base := opts.Concurrency
	if base <= 0 {
		base = DefaultConcurrency
	}
	max := opts.ConcurrencyMax
	if max <= 0 {
		max = DefaultConcurrencyMax
	}
	if max < base {
		max = base
	}
	retain := opts.RetentionDays
	if retain <= 0 {
		retain = DefaultRetentionDays
	}
	retainMax := opts.RetentionMax
	if retainMax <= 0 {
		retainMax = DefaultRetentionMax
	}
	var notifier *alert.Notifier
	if st != nil {
		notifier = alert.New(st)
	}
	var files *FileStore
	if strings.TrimSpace(opts.DataDir) != "" {
		files = NewFileStore(opts.DataDir)
	}
	return &Worker{
		Store:           st,
		Pool:            pool,
		Hosts:           hosts,
		Alerts:          notifier,
		Files:           files,
		baseConcurrency: base,
		maxConcurrency:  max,
		retentionDays:   retain,
		retentionMax:    retainMax,
		now:             time.Now,
		jitter:          RandomJitter,
	}
}

func (w *Worker) gate(clusterID string) chan struct{} {
	ch, _ := w.gates.LoadOrStore(clusterID, make(chan struct{}, 1))
	return ch.(chan struct{})
}

func (w *Worker) tryAcquireCluster(clusterID string) bool {
	ch := w.gate(clusterID)
	select {
	case ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (w *Worker) acquireCluster(ctx context.Context, clusterID string) error {
	ch := w.gate(clusterID)
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) releaseCluster(clusterID string) {
	ch := w.gate(clusterID)
	select {
	case <-ch:
	default:
	}
}

// Loop inspects due clusters with bounded parallelism until ctx is cancelled.
func (w *Worker) Loop(ctx context.Context) {
	if w == nil {
		return
	}
	if w.Alerts != nil {
		w.Alerts.OnFired = w.noteAlertFired
	}
	go w.dailyArchiveLoop(ctx)
	go w.recoveryProbeLoop(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		due, err := w.listDueIDs()
		if err != nil {
			log.Printf("inspect list: %v", err)
			if sleepCtx(ctx, Tick) != nil {
				return
			}
			continue
		}

		limit := EffectiveConcurrency(len(due), w.baseConcurrency, w.maxConcurrency)
		started := 0
		for _, id := range due {
			if int(w.inFlight.Load()) >= limit {
				break
			}
			// Skip busy clusters without burning MinStartGap (in-flight still appear in due).
			if !w.tryAcquireCluster(id) {
				continue
			}
			if !w.tryTakeSlot(limit) {
				w.releaseCluster(id)
				break
			}
			if err := w.reserveStart(ctx); err != nil {
				w.inFlight.Add(-1)
				w.releaseCluster(id)
				return
			}
			started++
			go func(clusterID string) {
				defer w.inFlight.Add(-1)
				defer w.releaseCluster(clusterID)
				if _, err := w.runClusterBody(ctx, clusterID, false, pushAsync); err != nil &&
					!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					log.Printf("inspect %s: %v", clusterID, err)
				}
			}(id)
		}

		if started == 0 {
			// Nothing is due: sleep the full tick even if a recovery probe holds a slot.
			// A 2s retry is only useful when due clusters were busy and should be retried.
			pause := MinStartGap
			if len(due) == 0 {
				pause = Tick
			}
			if sleepCtx(ctx, pause) != nil {
				return
			}
		}
	}
}

func (w *Worker) listDueIDs() ([]string, error) {
	clusters, err := w.Store.ListClusters()
	if err != nil {
		return nil, err
	}
	rows, err := w.Store.ListInspections()
	if err != nil {
		return nil, err
	}
	return ListDue(w.now(), clusters, inspectionMap(rows)), nil
}

// RunCluster probes kubeconfig then (on success) runs Diagnose. force bypasses MinAge.
// Same cluster cannot run twice at once (background or manual); waits for the gate.
func (w *Worker) RunCluster(ctx context.Context, clusterID string, force bool) (*Detail, error) {
	if w == nil || w.Store == nil {
		return nil, errors.New("inspect worker not configured")
	}
	if err := w.acquireCluster(ctx, clusterID); err != nil {
		return nil, err
	}
	defer w.releaseCluster(clusterID)
	return w.runClusterBody(ctx, clusterID, force, pushAsync)
}

func (w *Worker) reserveStart(ctx context.Context) error {
	w.startMu.Lock()
	defer w.startMu.Unlock()
	if !w.lastStart.IsZero() {
		if wait := MinStartGap - w.now().Sub(w.lastStart); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	w.lastStart = w.now()
	return nil
}

func (w *Worker) tryTakeSlot(limit int) bool {
	if limit < 1 {
		limit = 1
	}
	w.slotMu.Lock()
	defer w.slotMu.Unlock()
	if int(w.inFlight.Load()) >= limit {
		return false
	}
	w.inFlight.Add(1)
	return true
}

type pushMode int

const (
	pushAsync pushMode = iota
	pushSync
	pushLater // caller notifies after releasing the concurrency slot
)

func (w *Worker) runClusterBody(ctx context.Context, clusterID string, force bool, push pushMode) (*Detail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cl, err := w.Store.GetCluster(clusterID, true)
	if err != nil {
		return nil, err
	}
	if cl == nil {
		return nil, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !force {
		row, err := w.Store.GetInspection(clusterID)
		if err != nil {
			return nil, err
		}
		if row != nil && ShouldSkip(w.now(), row.CheckedAt) {
			d := detailFromRow(cl.Name, row)
			d.ClusterID = cl.ID
			d.ClusterName = cl.Name
			return d, nil
		}
	}
	if w.Alerts != nil {
		w.alertRound.Store(cl.ID, w.Alerts.NextRound(cl.ID))
	}

	if w.Hosts != nil {
		_, _ = w.Hosts.SyncIfChanged()
	}

	started := w.now()
	runCtx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()

	hosts := (*kube.HostsResolver)(nil)
	if w.Pool != nil {
		hosts = w.Pool.Hosts()
	}
	probe := kube.Probe(runCtx, cl.Kubeconfig, hosts)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	applyProbe(cl, probe, started)
	if err := w.Store.UpdateCluster(cl); err != nil {
		log.Printf("inspect update cluster %s: %v", cl.ID, err)
	}

	probeOK := strings.EqualFold(strings.TrimSpace(probe.Status), "ok")
	fireUC, recoverUC := w.noteReach(cl.ID, probeOK)

	issues := make([]kube.Issue, 0)
	var diag *kube.DiagnoseReport
	if !probeOK {
		if fireUC {
			issues = append(issues, kubeconfigIssue(cl, probe))
		}
	} else {
		issues, diag = w.diagnose(runCtx, cl)
	}
	sortIssues(issues)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	critical, errorN, warning, info := countIssues(issues)
	rep := Report{Probe: probe, Diagnose: diag, Issues: issues}
	raw, err := json.Marshal(rep)
	if err != nil {
		return nil, err
	}

	row := &store.InspectionRow{
		ClusterID:   cl.ID,
		CheckedAt:   started.UTC(),
		NextDue:     ComputeNextDue(started, w.jitter()).UTC(),
		ElapsedMs:   w.now().Sub(started).Milliseconds(),
		ProbeStatus: probe.Status,
		ProbeError:  probe.LastError,
		Critical:    critical,
		Error:       errorN,
		Warning:     warning,
		Info:        info,
		Summary:     makeSummary(probe, critical, errorN, warning, info),
		ReportJSON:  string(raw),
	}
	if err := w.Store.UpsertInspection(row); err != nil {
		return nil, err
	}
	if w.Files != nil {
		if err := w.Files.DumpRound(cl.Name, row, issues, beijingLoc); err != nil {
			log.Printf("inspect dump %s: %v", cl.ID, err)
		}
	}
	// Regular rounds push asynchronously so a slow webhook cannot stretch the patrol.
	// Recovery probes notify after releasing the slot, still before rescheduling.
	if push != pushLater {
		w.deliverAlerts(cl, probe.Status, diag, issues, push == pushSync, recoverUC)
	}
	d := detailFromRow(cl.Name, row)
	d.ClusterID = cl.ID
	d.ClusterName = cl.Name
	return d, nil
}

func (w *Worker) deliverAlerts(cl *model.Cluster, probeStatus string, diag *kube.DiagnoseReport, issues []kube.Issue, sync bool, recoverUnreachable bool) {
	if w == nil || w.Alerts == nil || cl == nil {
		return
	}
	if w.mediaMuted(cl) {
		return
	}
	round := 0
	if v, ok := w.alertRound.Load(cl.ID); ok {
		round, _ = v.(int)
	}
	gate := alert.RecoverGateFor(probeStatus, diag)
	if gate.OK && !recoverUnreachable {
		gate.HoldUnreachable = true
	}
	if sync {
		w.Alerts.Notify(cl, issues, gate, round)
		return
	}
	w.Alerts.NotifyAsync(cl, issues, gate, round)
}

// mediaMuted reports whether this cluster title should skip all media notify.
func (w *Worker) mediaMuted(cl *model.Cluster) bool {
	if w == nil || w.Store == nil || cl == nil {
		return false
	}
	mute := true
	if v, err := w.Store.GetAlertMuteTestClusters(); err != nil {
		log.Printf("alert mute-settings: %v", err)
	} else {
		mute = v
	}
	return alert.MuteMediaForCluster(cl.Name, mute)
}

func applyProbe(cl *model.Cluster, probe kube.ProbeResult, now time.Time) {
	cl.Server = probe.Server
	cl.PermType = probe.PermType
	cl.Namespaces = probe.Namespaces
	cl.Status = probe.Status
	cl.LastError = probe.LastError
	cl.FrontProxy = probe.FrontProxy
	cl.KubeVersion = probe.KubeVersion
	cl.UpdatedAt = now.UTC()
}

func (w *Worker) diagnose(ctx context.Context, cl *model.Cluster) ([]kube.Issue, *kube.DiagnoseReport) {
	if w.Pool == nil {
		return []kube.Issue{clientIssue(cl, errors.New("kube client pool missing"))}, nil
	}
	clients, err := w.Pool.Get(cl.ID, cl.Kubeconfig)
	if err != nil {
		return []kube.Issue{clientIssue(cl, err)}, nil
	}
	rep, err := clients.Diagnose(ctx, kube.DiagnoseOptions{
		PermType:   cl.PermType,
		FallbackNS: cl.Namespaces,
	})
	if err != nil {
		return []kube.Issue{diagnoseFailIssue(cl, err)}, nil
	}
	if rep == nil {
		return []kube.Issue{}, nil
	}
	issues := append([]kube.Issue(nil), rep.Issues...)
	return issues, rep
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
