package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"gitee.com/k8man/internal/hostsfile"
	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

// Worker runs per-cluster patrols on a 2h stagger and serves stored snapshots.
type Worker struct {
	Store *store.Store
	Pool  *kube.Pool
	Hosts *hostsfile.File

	now    func() time.Time
	jitter func() time.Duration
	mu     sync.Mutex
}

func New(st *store.Store, pool *kube.Pool, hosts *hostsfile.File) *Worker {
	return &Worker{
		Store:  st,
		Pool:   pool,
		Hosts:  hosts,
		now:    time.Now,
		jitter: RandomJitter,
	}
}

// Loop inspects due clusters one at a time until ctx is cancelled.
func (w *Worker) Loop(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		id, err := w.nextDueID()
		if err != nil {
			log.Printf("inspect list: %v", err)
			if sleepCtx(ctx, Tick) != nil {
				return
			}
			continue
		}
		if id == "" {
			if sleepCtx(ctx, Tick) != nil {
				return
			}
			continue
		}
		if _, err := w.RunCluster(ctx, id, false); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("inspect %s: %v", id, err)
		}
		if sleepCtx(ctx, MinGap) != nil {
			return
		}
	}
}

func (w *Worker) nextDueID() (string, error) {
	clusters, err := w.Store.ListClusters()
	if err != nil {
		return "", err
	}
	rows, err := w.Store.ListInspections()
	if err != nil {
		return "", err
	}
	return PickDue(w.now(), clusters, inspectionMap(rows)), nil
}

// RunCluster probes kubeconfig then (on success) runs Diagnose. force bypasses MinAge.
func (w *Worker) RunCluster(ctx context.Context, clusterID string, force bool) (*Detail, error) {
	if w == nil || w.Store == nil {
		return nil, errors.New("inspect worker not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()

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
	critical, warning := countIssues(issues)
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
		Warning:     warning,
		Summary:     makeSummary(probe, critical, warning),
		ReportJSON:  string(raw),
	}
	if err := w.Store.UpsertInspection(row); err != nil {
		return nil, err
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
