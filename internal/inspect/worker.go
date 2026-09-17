package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"gitee.com/k8man/internal/alert"
	"gitee.com/k8man/internal/hostsfile"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

// Options tunes background patrol concurrency.
type Options struct {
	Concurrency    int // base parallel slots (default 4)
	ConcurrencyMax int // hard cap when backlog expands (default 8)
}

// Worker runs per-cluster patrols on a 2h stagger and serves stored snapshots.
type Worker struct {
	Store  *store.Store
	Pool   *kube.Pool
	Hosts  *hostsfile.File
	Alerts *alert.Notifier

	baseConcurrency int
	maxConcurrency  int

	now    func() time.Time
	jitter func() time.Duration

	gates    sync.Map // clusterID -> chan struct{} (cap 1)
	inFlight atomic.Int32
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
	var notifier *alert.Notifier
	if st != nil {
		notifier = alert.New(st)
	}
	return &Worker{
		Store:           st,
		Pool:            pool,
		Hosts:           hosts,
		Alerts:          notifier,
		baseConcurrency: base,
		maxConcurrency:  max,
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
	var lastStart time.Time
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
			if !lastStart.IsZero() {
				if wait := MinStartGap - w.now().Sub(lastStart); wait > 0 {
					if sleepCtx(ctx, wait) != nil {
						w.releaseCluster(id)
						return
					}
				}
			}
			lastStart = w.now()
			w.inFlight.Add(1)
			started++
			go func(clusterID string) {
				defer w.inFlight.Add(-1)
				defer w.releaseCluster(clusterID)
				if _, err := w.runClusterBody(ctx, clusterID, false); err != nil &&
					!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					log.Printf("inspect %s: %v", clusterID, err)
				}
			}(id)
		}

		if started == 0 {
			pause := MinStartGap
			if len(due) == 0 && w.inFlight.Load() == 0 {
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
	return w.runClusterBody(ctx, clusterID, force)
}

func (w *Worker) runClusterBody(ctx context.Context, clusterID string, force bool) (*Detail, error) {
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

	issues := make([]kube.Issue, 0)
	var diag *kube.DiagnoseReport
	if probe.Status != "ok" {
		issues = append(issues, kubeconfigIssue(cl, probe))
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
	// Push after persist; Notifier uses its own timeout (does not depend on inspect ctx).
	if w.Alerts != nil {
		w.Alerts.NotifyCluster(ctx, cl, issues)
	}
	d := detailFromRow(cl.Name, row)
	d.ClusterID = cl.ID
	d.ClusterName = cl.Name
	return d, nil
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
