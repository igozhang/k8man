package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	diagnoseLargePodBytes    = int64(5) * 1024 * 1024 * 1024 // 5Gi ephemeral
	diagnoseNFSCriticalRatio = 0.80

	// Storage phase must not starve the rest of Diagnose / the 45s patrol.
	diagnoseStorageBudget    = 12 * time.Second
	diagnoseStorageMinRemain = 2500 * time.Millisecond
	diagnoseStorageSlack     = 400 * time.Millisecond

	diagnoseSummaryTimeout  = 1500 * time.Millisecond
	diagnoseSummaryWorkers  = 4
	diagnoseSummaryMaxNodes = 36

	diagnoseNFSPVCLimit      = 32
	diagnoseNFSPVCMatchMax   = 4
	diagnoseNFSPVCWorkers    = 6
	diagnoseNFSPVCGetTimeout = 2 * time.Second
	diagnoseSCListTimeout    = 3 * time.Second

	diagnoseNFSExecTimeout  = 2500 * time.Millisecond
	diagnoseNFSExecAttempts = 2

	diagnoseLargePodIssueCap = 40
)

// nodeStatsSummary is the subset of kubelet /stats/summary we need.
type nodeStatsSummary struct {
	Pods []podStatsSummary `json:"pods"`
}

type podStatsSummary struct {
	PodRef           podStatsRef            `json:"podRef"`
	EphemeralStorage *fsStats               `json:"ephemeral-storage"`
	Containers       []containerStatsSummary `json:"containers"`
	VolumeStats      []volumeStatsSummary   `json:"volume"`
}

type containerStatsSummary struct {
	Name   string   `json:"name"`
	Rootfs *fsStats `json:"rootfs"`
	Logs   *fsStats `json:"logs"`
}

type podStatsRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	UID       string `json:"uid"`
}

type fsStats struct {
	AvailableBytes *uint64 `json:"availableBytes"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	UsedBytes      *uint64 `json:"usedBytes"`
}

type volumeStatsSummary struct {
	Name           string  `json:"name"`
	UsedBytes      *uint64 `json:"usedBytes"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	AvailableBytes *uint64 `json:"availableBytes"`
	PVCRef         *pvcRef `json:"pvcRef"`
}

type pvcRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type nfsSCPick struct {
	Name   string
	Source string // default | unique
	SC     storagev1.StorageClass
}

type nfsUsageResult struct {
	Percent       float64
	UsedBytes     int64
	CapacityBytes int64
	StorageClass  string
	Server        string
	Path          string
	Source        string
	Probe         string
	Skipped       string
	Issue         *Issue
}

func storageScanContext(ctx context.Context) (context.Context, context.CancelFunc, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, func() {}, "parent cancelled"
	}
	budget := diagnoseStorageBudget
	if dl, ok := ctx.Deadline(); ok {
		remain := time.Until(dl)
		if remain < diagnoseStorageMinRemain {
			return nil, func() {}, "timeout budget exhausted"
		}
		if remain < budget+diagnoseStorageSlack {
			budget = remain - diagnoseStorageSlack
			if budget < diagnoseStorageMinRemain {
				return nil, func() {}, "timeout budget exhausted"
			}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	return cctx, cancel, ""
}

// scanStorage fills ephemeral large-pod + default/unique NFS usage into scan and returns issues.
// Best-effort: respects a hard time budget and never blocks Diagnose on hung kubelet proxies.
func (c *Clients) scanStorage(ctx context.Context, opt DiagnoseOptions, pods []corev1.Pod, scan *DiagnoseScan) ([]Issue, bool) {
	if scan == nil {
		scan = &DiagnoseScan{}
	}
	if opt.PermType == "project" {
		scan.LargePodSkipped = "project credential"
		scan.NFSSkipped = "project credential"
		return nil, false
	}
	if c == nil || c.Clientset == nil {
		scan.LargePodSkipped = "clients not ready"
		scan.NFSSkipped = "clients not ready"
		return nil, false
	}

	sctx, cancel, skip := storageScanContext(ctx)
	if skip != "" {
		scan.LargePodSkipped = skip
		scan.NFSSkipped = skip
		return nil, false
	}
	defer cancel()

	var (
		issues []Issue
		trunc  bool
	)

	summaries, sumTrunc, sumErr := c.fetchNodeSummaries(sctx, pods)
	if sumErr != nil && len(summaries) == 0 {
		scan.LargePodSkipped = clipRunes(sumErr.Error(), 240)
	} else if len(summaries) == 0 {
		if sctx.Err() != nil {
			scan.LargePodSkipped = "timeout budget exhausted"
		} else {
			scan.LargePodSkipped = "no kubelet summary"
		}
	} else {
		large := largePodIssuesFromSummaries(summaries)
		scan.LargePods = len(large)
		if len(large) > diagnoseLargePodIssueCap {
			large = large[:diagnoseLargePodIssueCap]
			trunc = true
		}
		issues = append(issues, large...)
		if sumTrunc {
			trunc = true
		}
	}

	if sctx.Err() != nil {
		if scan.NFSSkipped == "" {
			scan.NFSSkipped = "timeout budget exhausted"
		}
		return issues, trunc
	}

	nfs := c.probeNFSUsage(sctx, pods, summaries)
	scan.NFSStorageClass = nfs.StorageClass
	scan.NFSServer = sanitizeNFSServer(nfs.Server)
	scan.NFSPath = sanitizeNFSPath(nfs.Path)
	scan.NFSSource = nfs.Source
	scan.NFSProbe = nfs.Probe
	scan.NFSSkipped = nfs.Skipped
	scan.NFSUsedBytes = nfs.UsedBytes
	scan.NFSCapacityBytes = nfs.CapacityBytes
	if nfs.Skipped == "" && nfs.CapacityBytes > 0 {
		p := nfs.Percent
		scan.NFSUsagePercent = &p
	}
	if nfs.Issue != nil {
		issues = append(issues, *nfs.Issue)
	}
	return issues, trunc
}

func largePodIssuesFromSummaries(summaries map[string]nodeStatsSummary) []Issue {
	var out []Issue
	seen := map[string]bool{}
	for nodeName, sum := range summaries {
		for _, p := range sum.Pods {
			ns := strings.TrimSpace(p.PodRef.Namespace)
			name := strings.TrimSpace(p.PodRef.Name)
			if ns == "" || name == "" {
				continue
			}
			used, ok := podNonPersistentUsedBytes(p)
			if !ok || used <= diagnoseLargePodBytes {
				continue
			}
			key := ns + "/" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			msg := fmt.Sprintf("非持久化存储占用 %s，超过 5Gi", fmtBytes(float64(used)))
			if nodeName != "" {
				msg += " · 节点 " + nodeName
			}
			out = append(out, Issue{
				Severity:     sevWarning,
				BaseSeverity: sevWarning,
				Category:     "storage",
				Kind:         "Pod",
				Namespace:    ns,
				Name:         name,
				Reason:       "LargeEphemeral",
				Message:      msg,
				Node:         nodeName,
				Evidence: clipEvidence([]string{
					fmt.Sprintf("ephemeralUsed=%s", fmtBytes(float64(used))),
					"threshold=5Gi",
					"source=kubelet-summary",
					"node=" + nodeName,
				}),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// PodEphemeralUsage is one Pod's non-persistent (ephemeral) storage usage from kubelet summary.
type PodEphemeralUsage struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Node      string `json:"node"`
	UsedBytes int64  `json:"usedBytes"`
}

// ListPodEphemeralUsage returns non-persistent storage usage for Pods in ns (full list, not only >5Gi).
// skipped explains soft failures (project credential, timeout, no summary); trunc is partial node coverage.
func (c *Clients) ListPodEphemeralUsage(ctx context.Context, ns, permType string) (items []PodEphemeralUsage, skipped string, trunc bool, err error) {
	ns = strings.TrimSpace(ns)
	if ns == "" {
		return nil, "namespace required", false, nil
	}
	if permType == "project" {
		return nil, "project credential", false, nil
	}
	if c == nil || c.Clientset == nil {
		return nil, "clients not ready", false, nil
	}

	pods, listErr := c.ListPods(ctx, ns)
	if listErr != nil {
		return nil, "", false, listErr
	}
	// NS 内无已调度 Pod 时不要回退扫全集群节点（diagnose 的 fetchNodeSummaries 会这么做）。
	if len(nodeNamesForSummary(pods)) == 0 {
		return []PodEphemeralUsage{}, "", false, nil
	}

	sctx, cancel, skip := storageScanContext(ctx)
	if skip != "" {
		return nil, skip, false, nil
	}
	defer cancel()

	summaries, sumTrunc, sumErr := c.fetchNodeSummaries(sctx, pods)
	if sumErr != nil && len(summaries) == 0 {
		return nil, clipRunes(sumErr.Error(), 240), sumTrunc, nil
	}
	if len(summaries) == 0 {
		if sctx.Err() != nil {
			return nil, "timeout budget exhausted", sumTrunc, nil
		}
		return nil, "no kubelet summary", sumTrunc, nil
	}
	items = podEphemeralUsagesFromSummaries(summaries, ns)
	return items, "", sumTrunc, nil
}

// podEphemeralUsagesFromSummaries lists all Pods with measurable non-persistent usage in ns (empty ns = all).
func podEphemeralUsagesFromSummaries(summaries map[string]nodeStatsSummary, ns string) []PodEphemeralUsage {
	var out []PodEphemeralUsage
	seen := map[string]bool{}
	for nodeName, sum := range summaries {
		for _, p := range sum.Pods {
			pns := strings.TrimSpace(p.PodRef.Namespace)
			name := strings.TrimSpace(p.PodRef.Name)
			if pns == "" || name == "" {
				continue
			}
			if ns != "" && pns != ns {
				continue
			}
			used, ok := podNonPersistentUsedBytes(p)
			if !ok {
				continue
			}
			key := pns + "/" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, PodEphemeralUsage{
				Name:      name,
				Namespace: pns,
				Node:      nodeName,
				UsedBytes: used,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UsedBytes != out[j].UsedBytes {
			return out[i].UsedBytes > out[j].UsedBytes
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// podNonPersistentUsedBytes reads non-PVC usage from kubelet summary.
// Use pod ephemeral-storage when it reports a positive value; otherwise fall back to
// container rootfs/logs + non-PVC volumes (covers missing/zero pod-level fields).
func podNonPersistentUsedBytes(p podStatsSummary) (int64, bool) {
	var sum int64
	hasParts := false
	for _, c := range p.Containers {
		if c.Rootfs != nil && c.Rootfs.UsedBytes != nil {
			sum += int64(*c.Rootfs.UsedBytes)
			hasParts = true
		}
		if c.Logs != nil && c.Logs.UsedBytes != nil {
			sum += int64(*c.Logs.UsedBytes)
			hasParts = true
		}
	}
	for _, vol := range p.VolumeStats {
		if vol.PVCRef != nil || vol.UsedBytes == nil {
			continue
		}
		sum += int64(*vol.UsedBytes)
		hasParts = true
	}
	if p.EphemeralStorage != nil && p.EphemeralStorage.UsedBytes != nil {
		eph := int64(*p.EphemeralStorage.UsedBytes)
		if eph > 0 {
			return eph, true
		}
		if hasParts {
			return sum, true
		}
		return 0, true
	}
	return sum, hasParts
}

func (c *Clients) fetchNodeSummaries(ctx context.Context, pods []corev1.Pod) (map[string]nodeStatsSummary, bool, error) {
	nodes := nodeNamesForSummary(pods)
	if len(nodes) == 0 {
		nctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		listed, _, err := c.listNodesPaged(nctx, DiagnoseOptions{}, diagnoseSummaryMaxNodes)
		cancel()
		if err != nil {
			return nil, false, err
		}
		for _, n := range listed {
			nodes = append(nodes, n.Name)
		}
	}
	trunc := false
	if len(nodes) > diagnoseSummaryMaxNodes {
		nodes = nodes[:diagnoseSummaryMaxNodes]
		trunc = true
	}
	if len(nodes) == 0 {
		return map[string]nodeStatsSummary{}, trunc, nil
	}

	out := make(map[string]nodeStatsSummary, len(nodes))
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, diagnoseSummaryWorkers)
	var wg sync.WaitGroup

	for _, name := range nodes {
		if ctx.Err() != nil {
			trunc = true
			break
		}
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				mu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				mu.Unlock()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			nctx, cancel := context.WithTimeout(ctx, diagnoseSummaryTimeout)
			sum, err := c.getNodeStatsSummary(nctx, name)
			cancel()
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			out[name] = sum
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Prefer partial success: hung nodes must not wipe good samples.
	if len(out) > 0 {
		if len(out) < len(nodes) {
			trunc = true
		}
		return out, trunc, nil
	}
	if firstErr != nil {
		return map[string]nodeStatsSummary{}, trunc, firstErr
	}
	return out, trunc, nil
}

func nodeNamesForSummary(pods []corev1.Pod) []string {
	seen := map[string]bool{}
	var names []string
	for _, p := range pods {
		n := strings.TrimSpace(p.Spec.NodeName)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
		if len(names) >= diagnoseSummaryMaxNodes {
			break
		}
	}
	return names
}

func (c *Clients) getNodeStatsSummary(ctx context.Context, nodeName string) (nodeStatsSummary, error) {
	var zero nodeStatsSummary
	raw, err := c.Clientset.CoreV1().RESTClient().Get().
		Resource("nodes").
		Name(nodeName).
		SubResource("proxy").
		Suffix("stats/summary").
		Do(ctx).
		Raw()
	if err != nil {
		return zero, err
	}
	var sum nodeStatsSummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		return zero, err
	}
	return sum, nil
}

func (c *Clients) probeNFSUsage(ctx context.Context, pods []corev1.Pod, summaries map[string]nodeStatsSummary) nfsUsageResult {
	if ctx.Err() != nil {
		return nfsUsageResult{Skipped: "timeout budget exhausted"}
	}
	sctx, cancel := context.WithTimeout(ctx, diagnoseSCListTimeout)
	scs, err := c.Clientset.StorageV1().StorageClasses().List(sctx, diagnoseListOpts("", 200, ""))
	cancel()
	if err != nil {
		msg := clipRunes(err.Error(), 240)
		if apierrors.IsTimeout(err) || ctx.Err() != nil {
			msg = "timeout listing StorageClass"
		}
		return nfsUsageResult{Skipped: msg}
	}
	pick, skip := pickNFSStorageClass(scs.Items)
	if skip != "" {
		return nfsUsageResult{Skipped: skip}
	}

	res := nfsUsageResult{
		StorageClass: pick.Name,
		Source:       pick.Source,
	}

	// Prefer volume stats already present in kubelet summaries (no extra node I/O).
	if used, cap, matched, ok := nfsUsageFromSummaries(ctx, c, summaries, pods, pick.Name); ok && cap > 0 {
		res.UsedBytes = used
		res.CapacityBytes = cap
		res.Percent = float64(used) / float64(cap) * 100
		res.Probe = "summary"
		res.Server, res.Path = c.resolveNFSEndpoint(ctx, pick, pods, matched, "")
		// Summary has usage but often no NFS endpoint — one short df pass for server:/path only.
		// Skip when budget is too tight so hung exec cannot starve the rest of storage scan.
		if res.Server == "" && nfsEndpointDFBudgetOK(ctx) {
			if _, _, fs, okDF := c.nfsUsageViaExecDF(ctx, pods, pick.Name); okDF {
				if s, p := parseNFSFilesystem(fs); s != "" {
					res.Server, res.Path = mergeNFSEndpoint(res.Server, res.Path, s, p)
				}
			}
		}
		if res.Percent/100 >= diagnoseNFSCriticalRatio {
			res.Issue = nfsHighUsageIssue(pick, res)
		}
		return res
	}

	if ctx.Err() != nil {
		res.Skipped = "timeout before NFS exec fallback"
		return res
	}

	// Fallback: at most a couple short exec df attempts.
	if used, cap, fs, ok := c.nfsUsageViaExecDF(ctx, pods, pick.Name); ok && cap > 0 {
		res.UsedBytes = used
		res.CapacityBytes = cap
		res.Percent = float64(used) / float64(cap) * 100
		res.Probe = "exec-df"
		res.Server, res.Path = c.resolveNFSEndpoint(ctx, pick, pods, nil, fs)
		if res.Percent/100 >= diagnoseNFSCriticalRatio {
			res.Issue = nfsHighUsageIssue(pick, res)
		}
		return res
	}

	if ctx.Err() != nil {
		res.Skipped = "timeout budget exhausted"
		return res
	}
	// Still try to surface endpoint even when usage unknown (helps ops).
	res.Server, res.Path = c.resolveNFSEndpoint(ctx, pick, pods, nil, "")
	res.Skipped = "no mounted PVC of target SC"
	return res
}

func nfsHighUsageIssue(pick nfsSCPick, res nfsUsageResult) *Issue {
	server := sanitizeNFSServer(res.Server)
	path := sanitizeNFSPath(res.Path)
	target := FormatNFSEndpoint(server, path)
	if target == "" {
		target = "SC=" + pick.Name
	}
	msg := fmt.Sprintf("NFS %s 使用率 %.0f%%（%s / %s），超过 80%%",
		target, res.Percent, fmtBytes(float64(res.UsedBytes)), fmtBytes(float64(res.CapacityBytes)))
	ev := []string{}
	if server != "" {
		ev = append(ev, "server="+server)
	}
	if path != "" {
		ev = append(ev, "path="+path)
	}
	ev = append(ev,
		fmt.Sprintf("usage=%.1f%%", res.Percent),
		fmt.Sprintf("used=%s", fmtBytes(float64(res.UsedBytes))),
		fmt.Sprintf("capacity=%s", fmtBytes(float64(res.CapacityBytes))),
		"threshold=80%",
		"sc="+pick.Name,
		"scSource="+pick.Source,
		"probe="+res.Probe,
		"provisioner="+pick.SC.Provisioner,
	)
	it := Issue{
		Severity:     sevCritical,
		BaseSeverity: sevCritical,
		Category:     "storage",
		Kind:         "StorageClass",
		Name:         pick.Name,
		Reason:       "NFSHighUsage",
		Message:      msg,
		NodeIP:       server, // reuse for alert/UI location (NFS server, not a node)
		Evidence:     clipEvidence(ev),
	}
	return &it
}

// nfsEndpointDFBudgetOK reports whether the storage context still has room for one df exec attempt.
func nfsEndpointDFBudgetOK(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	need := diagnoseNFSExecTimeout + diagnoseStorageSlack
	dl, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(dl) >= need
}

// sanitizeNFSServer drops empty / whitespace / absurd values from SC params or mount options.
func sanitizeNFSServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 253 {
		return ""
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return ""
	}
	// 0.0.0.0 in mountOptions addr= is a placeholder, not a reachable NFS endpoint.
	if s == "0.0.0.0" || s == "::" {
		return ""
	}
	return s
}

func sanitizeNFSPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || len(p) > 512 {
		return ""
	}
	if strings.ContainsAny(p, "\t\r\n") {
		return ""
	}
	return p
}

// nfsEndpointFromParams reads common StorageClass / CSI parameter keys used by NFS provisioners.
func nfsEndpointFromParams(params map[string]string) (server, path string) {
	if len(params) == 0 {
		return "", ""
	}
	for _, k := range []string{
		"server", "nfsServer", "nfs.server", "nfs-server", "nfs_server",
		"addr", "address", "host", "hostname", "serverAddr", "serverAddress",
	} {
		if v := sanitizeNFSServer(params[k]); v != "" {
			server = v
			break
		}
	}
	for _, k := range []string{
		"path", "share", "nfsPath", "nfs.path", "nfs-path", "nfs_path",
		"export", "baseDir", "basePath",
	} {
		// Prefer real export roots; skip bare "subdir" (often a template token, not the share).
		if v := sanitizeNFSPath(params[k]); v != "" {
			path = v
			break
		}
	}
	return server, path
}

// nfsEndpointFromMountOptions parses addr= from PV/SC mountOptions (and comma-joined option blobs).
func nfsEndpointFromMountOptions(opts []string) (server, path string) {
	for _, o := range opts {
		for _, part := range strings.Split(o, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			low := strings.ToLower(part)
			if strings.HasPrefix(low, "addr=") {
				if v := sanitizeNFSServer(part[len("addr="):]); v != "" {
					server = v
				}
			}
		}
	}
	return server, path
}

func mergeNFSEndpoint(server, path, addServer, addPath string) (string, string) {
	if strings.TrimSpace(server) == "" {
		server = sanitizeNFSServer(addServer)
	}
	if strings.TrimSpace(path) == "" {
		path = sanitizeNFSPath(addPath)
	}
	return server, path
}

func nfsEndpointFromPV(pv *corev1.PersistentVolume) (server, path string) {
	if pv == nil {
		return "", ""
	}
	if pv.Spec.NFS != nil {
		server = sanitizeNFSServer(pv.Spec.NFS.Server)
		path = sanitizeNFSPath(pv.Spec.NFS.Path)
	}
	if pv.Spec.CSI != nil && len(pv.Spec.CSI.VolumeAttributes) > 0 {
		s, p := nfsEndpointFromParams(pv.Spec.CSI.VolumeAttributes)
		server, path = mergeNFSEndpoint(server, path, s, p)
	}
	s, p := nfsEndpointFromMountOptions(pv.Spec.MountOptions)
	return mergeNFSEndpoint(server, path, s, p)
}

// parseNFSFilesystem parses df Filesystem like "192.168.1.1:/export" or "nfs.example.com:/data".
func parseNFSFilesystem(fs string) (server, path string) {
	fs = strings.TrimSpace(fs)
	if fs == "" {
		return "", ""
	}
	// host:/path  (NFS classic); also works for IPv6 "2001:db8::1:/export"
	if i := strings.Index(fs, ":/"); i > 0 {
		return sanitizeNFSServer(fs[:i]), sanitizeNFSPath(fs[i+1:])
	}
	// host:path without leading slash on path (avoid Windows drive letters like C:)
	if i := strings.IndexByte(fs, ':'); i > 1 {
		host := strings.TrimSpace(fs[:i])
		rest := strings.TrimSpace(fs[i+1:])
		if host != "" && rest != "" && !strings.Contains(host, "/") {
			if !strings.HasPrefix(rest, "/") {
				rest = "/" + rest
			}
			return sanitizeNFSServer(host), sanitizeNFSPath(rest)
		}
	}
	return "", ""
}

// FormatNFSEndpoint builds server:/path for alerts and UI.
func FormatNFSEndpoint(server, path string) string {
	server = sanitizeNFSServer(server)
	path = sanitizeNFSPath(path)
	if server == "" {
		return ""
	}
	if path == "" {
		return server
	}
	if strings.HasPrefix(path, "/") {
		return server + ":" + path
	}
	return server + ":/" + path
}

// resolveNFSEndpoint tries SC parameters/mountOptions, df filesystem, matched PVC/PV, pod PVCs, then PV list.
// Fills server and path independently so a path-only SC param can combine with a PV server.
func (c *Clients) resolveNFSEndpoint(ctx context.Context, pick nfsSCPick, pods []corev1.Pod, matched *pvcRef, fsFromDF string) (server, path string) {
	server, path = nfsEndpointFromParams(pick.SC.Parameters)
	{
		s, p := nfsEndpointFromMountOptions(pick.SC.MountOptions)
		server, path = mergeNFSEndpoint(server, path, s, p)
	}
	if server != "" && path != "" {
		return server, path
	}
	if fsFromDF != "" {
		s, p := parseNFSFilesystem(fsFromDF)
		server, path = mergeNFSEndpoint(server, path, s, p)
		if server != "" && path != "" {
			return server, path
		}
	}
	if matched != nil {
		s, p := c.lookupNFSEndpointFromPVCRef(ctx, *matched)
		server, path = mergeNFSEndpoint(server, path, s, p)
		if server != "" && path != "" {
			return server, path
		}
	}
	if ctx.Err() == nil {
		s, p := c.lookupNFSEndpointFromPVCs(ctx, pods, pick.Name)
		server, path = mergeNFSEndpoint(server, path, s, p)
		if server != "" && path != "" {
			return server, path
		}
	}
	if ctx.Err() == nil {
		s, p := c.lookupNFSEndpointFromPVList(ctx, pick.Name)
		server, path = mergeNFSEndpoint(server, path, s, p)
	}
	return server, path
}

func (c *Clients) lookupNFSEndpointFromPVCRef(ctx context.Context, ref pvcRef) (server, path string) {
	if c == nil || c.Clientset == nil || ref.Namespace == "" || ref.Name == "" || ctx.Err() != nil {
		return "", ""
	}
	gctx, cancel := context.WithTimeout(ctx, diagnoseNFSPVCGetTimeout)
	pvc, err := c.Clientset.CoreV1().PersistentVolumeClaims(ref.Namespace).Get(gctx, ref.Name, metav1.GetOptions{})
	cancel()
	if err != nil || strings.TrimSpace(pvc.Spec.VolumeName) == "" {
		return "", ""
	}
	gctx2, cancel2 := context.WithTimeout(ctx, diagnoseNFSPVCGetTimeout)
	pv, err := c.Clientset.CoreV1().PersistentVolumes().Get(gctx2, pvc.Spec.VolumeName, metav1.GetOptions{})
	cancel2()
	if err != nil {
		return "", ""
	}
	return nfsEndpointFromPV(pv)
}

// lookupNFSEndpointFromPVCs GETs a few Bound PVCs of scName and reads NFS server/path from the PV.
func (c *Clients) lookupNFSEndpointFromPVCs(ctx context.Context, pods []corev1.Pod, scName string) (server, path string) {
	if c == nil || c.Clientset == nil || scName == "" || ctx.Err() != nil {
		return "", ""
	}
	claims := filterPVCsBySC(ctx, c, pvcRefsFromPods(pods), scName, diagnoseNFSPVCMatchMax)
	for _, ref := range claims {
		if ctx.Err() != nil {
			break
		}
		if s, p := c.lookupNFSEndpointFromPVCRef(ctx, ref); s != "" {
			return s, p
		}
	}
	return "", ""
}

func (c *Clients) lookupNFSEndpointFromPVList(ctx context.Context, scName string) (server, path string) {
	if c == nil || c.Clientset == nil || scName == "" || ctx.Err() != nil {
		return "", ""
	}
	gctx, cancel := context.WithTimeout(ctx, diagnoseSCListTimeout)
	pvs, err := c.Clientset.CoreV1().PersistentVolumes().List(gctx, metav1.ListOptions{Limit: 200})
	cancel()
	if err != nil {
		return "", ""
	}
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		if pv.Status.Phase != corev1.VolumeBound {
			continue
		}
		if pvStorageClassName(*pv) != scName {
			continue
		}
		if s, p := nfsEndpointFromPV(pv); s != "" {
			return s, p
		}
	}
	return "", ""
}

func pvStorageClassName(pv corev1.PersistentVolume) string {
	if pv.Spec.StorageClassName != "" {
		return strings.TrimSpace(pv.Spec.StorageClassName)
	}
	if pv.Annotations != nil {
		if n := strings.TrimSpace(pv.Annotations["volume.beta.kubernetes.io/storage-class"]); n != "" {
			return n
		}
	}
	return ""
}

func pickNFSStorageClass(scs []storagev1.StorageClass) (nfsSCPick, string) {
	if len(scs) == 0 {
		return nfsSCPick{}, "no StorageClass"
	}
	var defaults []storagev1.StorageClass
	for _, sc := range scs {
		if isDefaultStorageClass(sc) {
			defaults = append(defaults, sc)
		}
	}
	if len(defaults) > 0 {
		for _, sc := range defaults {
			if isNFSSC(sc) {
				return nfsSCPick{Name: sc.Name, Source: "default", SC: sc}, ""
			}
		}
		return nfsSCPick{}, "default SC is not NFS"
	}
	if len(scs) == 1 {
		sc := scs[0]
		if !isNFSSC(sc) {
			return nfsSCPick{}, "unique SC is not NFS"
		}
		return nfsSCPick{Name: sc.Name, Source: "unique", SC: sc}, ""
	}
	return nfsSCPick{}, "no default SC and multiple SCs"
}

func isDefaultStorageClass(sc storagev1.StorageClass) bool {
	if sc.Annotations == nil {
		return false
	}
	v := sc.Annotations["storageclass.kubernetes.io/is-default-class"]
	if v == "" {
		v = sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"]
	}
	return strings.EqualFold(strings.TrimSpace(v), "true")
}

func isNFSSC(sc storagev1.StorageClass) bool {
	p := strings.ToLower(strings.TrimSpace(sc.Provisioner))
	name := strings.ToLower(strings.TrimSpace(sc.Name))

	// Strong signal: known NFS provisioners.
	if isKnownNFSProvisioner(p) {
		return true
	}
	// Name hint (e.g. managed-nfs-storage), but never override an obvious non-NFS driver.
	if nameLooksLikeNFS(name) && !isKnownNonNFSProvisioner(p) {
		return true
	}
	return false
}

func isKnownNFSProvisioner(p string) bool {
	if p == "" {
		return false
	}
	if strings.Contains(p, "nfs") {
		return true
	}
	// nfs-client-provisioner / nfs-subdir-external-provisioner historical default.
	return p == "fuseim.pri/ifs"
}

func nameLooksLikeNFS(name string) bool {
	if name == "" || !strings.Contains(name, "nfs") {
		return false
	}
	// Avoid obvious negatives: non-nfs / not-nfs / no-nfs.
	for _, neg := range []string{"non-nfs", "not-nfs", "no-nfs"} {
		if strings.Contains(name, neg) {
			return false
		}
	}
	return true
}

func isKnownNonNFSProvisioner(p string) bool {
	if p == "" {
		return false
	}
	for _, s := range []string{
		"local-path", "no-provisioner", "hostpath", "local.csi",
		"ebs.csi", "aws-ebs", "gce-pd", "azure-disk", "azure-file",
		"cinder", "vsphere", "ceph", "rbd", "longhorn", "openebs",
		"portworx", "lvm.csi",
	} {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

func pvcStorageClassName(pvc corev1.PersistentVolumeClaim) string {
	if pvc.Spec.StorageClassName != nil {
		if n := strings.TrimSpace(*pvc.Spec.StorageClassName); n != "" {
			return n
		}
	}
	if pvc.Annotations != nil {
		if n := strings.TrimSpace(pvc.Annotations["volume.beta.kubernetes.io/storage-class"]); n != "" {
			return n
		}
		if n := strings.TrimSpace(pvc.Annotations["volume.kubernetes.io/storage-class"]); n != "" {
			return n
		}
	}
	return ""
}

type volSample struct {
	Ref  pvcRef
	Used int64
	Cap  int64
}

func volumeSamplesFromSummaries(summaries map[string]nodeStatsSummary) []volSample {
	var out []volSample
	seen := map[string]bool{}
	for _, sum := range summaries {
		for _, p := range sum.Pods {
			for _, vol := range p.VolumeStats {
				if vol.PVCRef == nil || vol.UsedBytes == nil || vol.CapacityBytes == nil {
					continue
				}
				cap := int64(*vol.CapacityBytes)
				if cap <= 0 {
					continue
				}
				ref := pvcRef{
					Namespace: strings.TrimSpace(vol.PVCRef.Namespace),
					Name:      strings.TrimSpace(vol.PVCRef.Name),
				}
				if ref.Namespace == "" || ref.Name == "" {
					continue
				}
				key := ref.Namespace + "/" + ref.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, volSample{Ref: ref, Used: int64(*vol.UsedBytes), Cap: cap})
				if len(out) >= diagnoseNFSPVCLimit {
					return out
				}
			}
		}
	}
	return out
}

func nfsUsageFromSummaries(ctx context.Context, c *Clients, summaries map[string]nodeStatsSummary, pods []corev1.Pod, scName string) (used, capacity int64, matched *pvcRef, ok bool) {
	samples := volumeSamplesFromSummaries(summaries)
	if len(samples) > 0 {
		refs := make([]pvcRef, 0, len(samples))
		byKey := map[string]volSample{}
		for _, s := range samples {
			refs = append(refs, s.Ref)
			byKey[s.Ref.Namespace+"/"+s.Ref.Name] = s
		}
		matchedRefs := filterPVCsBySC(ctx, c, refs, scName, 1)
		if len(matchedRefs) > 0 {
			ref := matchedRefs[0]
			s := byKey[ref.Namespace+"/"+ref.Name]
			refCopy := ref
			return s.Used, s.Cap, &refCopy, true
		}
	}

	// No volume stats for target SC yet: resolve a few claims from Running pods, then re-check samples.
	claims := filterPVCsBySC(ctx, c, pvcRefsFromPods(pods), scName, diagnoseNFSPVCMatchMax)
	if len(claims) == 0 {
		return 0, 0, nil, false
	}
	want := map[string]bool{}
	for _, ref := range claims {
		want[ref.Namespace+"/"+ref.Name] = true
	}
	for _, s := range samples {
		key := s.Ref.Namespace + "/" + s.Ref.Name
		if want[key] {
			refCopy := s.Ref
			return s.Used, s.Cap, &refCopy, true
		}
	}
	return 0, 0, nil, false
}

func pvcRefsFromPods(pods []corev1.Pod) []pvcRef {
	seen := map[string]bool{}
	var out []pvcRef
	for _, p := range pods {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				continue
			}
			name := strings.TrimSpace(v.PersistentVolumeClaim.ClaimName)
			if name == "" {
				continue
			}
			key := p.Namespace + "/" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, pvcRef{Namespace: p.Namespace, Name: name})
			if len(out) >= diagnoseNFSPVCLimit {
				return out
			}
		}
	}
	return out
}

// filterPVCsBySC GETs candidates in parallel and returns Bound PVCs on scName (capped).
func filterPVCsBySC(ctx context.Context, c *Clients, refs []pvcRef, scName string, maxMatch int) []pvcRef {
	if c == nil || c.Clientset == nil || scName == "" || maxMatch <= 0 || len(refs) == 0 {
		return nil
	}
	if len(refs) > diagnoseNFSPVCLimit {
		refs = refs[:diagnoseNFSPVCLimit]
	}

	var (
		mu      sync.Mutex
		matched []pvcRef
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, diagnoseNFSPVCWorkers)
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	for _, ref := range refs {
		ref := ref
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			default:
			}

			gctx, cancel := context.WithTimeout(ctx, diagnoseNFSPVCGetTimeout)
			pvc, err := c.Clientset.CoreV1().PersistentVolumeClaims(ref.Namespace).Get(gctx, ref.Name, metav1.GetOptions{})
			cancel()
			if err != nil || pvc.Status.Phase != corev1.ClaimBound {
				return
			}
			if pvcStorageClassName(*pvc) != scName {
				return
			}
			mu.Lock()
			if len(matched) < maxMatch {
				matched = append(matched, ref)
				if len(matched) >= maxMatch {
					closeDone()
				}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return matched
}

func (c *Clients) nfsUsageViaExecDF(ctx context.Context, pods []corev1.Pod, scName string) (used, capacity int64, filesystem string, ok bool) {
	claims := filterPVCsBySC(ctx, c, pvcRefsFromPods(pods), scName, diagnoseNFSPVCMatchMax)
	if len(claims) == 0 {
		return 0, 0, "", false
	}
	claimSet := map[string]bool{}
	for _, ref := range claims {
		claimSet[ref.Namespace+"/"+ref.Name] = true
	}
	attempts := 0
	for _, p := range pods {
		if ctx.Err() != nil || attempts >= diagnoseNFSExecAttempts {
			break
		}
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		mountPath, container := pvcMountInPod(p, claimSet)
		if mountPath == "" {
			continue
		}
		attempts++
		ectx, cancel := context.WithTimeout(ctx, diagnoseNFSExecTimeout)
		var stdout, stderr bytes.Buffer
		err := c.Exec(ectx, ExecOptions{
			Namespace: p.Namespace,
			Pod:       p.Name,
			Container: container,
			Command:   []string{"df", "-P", mountPath},
			Stdout:    &stdout,
			Stderr:    &stderr,
		})
		cancel()
		if err != nil {
			continue
		}
		u, cap, fs, parsed := parseDFPOutput(stdout.String())
		if !parsed || cap <= 0 {
			continue
		}
		return u, cap, fs, true
	}
	return 0, 0, "", false
}

func pvcMountInPod(p corev1.Pod, claimSet map[string]bool) (mountPath, container string) {
	volName := ""
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		key := p.Namespace + "/" + strings.TrimSpace(v.PersistentVolumeClaim.ClaimName)
		if claimSet[key] {
			volName = v.Name
			break
		}
	}
	if volName == "" {
		return "", ""
	}
	for _, c := range p.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if m.Name == volName && strings.TrimSpace(m.MountPath) != "" {
				return m.MountPath, c.Name
			}
		}
	}
	return "", ""
}

// parseDFPOutput parses `df -P <path>` (1024-block POSIX). Returns used/capacity bytes and Filesystem field.
func parseDFPOutput(out string) (used, capacity int64, filesystem string, ok bool) {
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToLower(line), "filesystem") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		blocks, err1 := strconv.ParseInt(fields[1], 10, 64)
		usedBlocks, err2 := strconv.ParseInt(fields[2], 10, 64)
		if err1 != nil || err2 != nil || blocks <= 0 {
			continue
		}
		return usedBlocks * 1024, blocks * 1024, fields[0], true
	}
	return 0, 0, "", false
}
