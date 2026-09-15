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
	PodRef           podStatsRef          `json:"podRef"`
	EphemeralStorage *fsStats             `json:"ephemeral-storage"`
	VolumeStats      []volumeStatsSummary `json:"volume"`
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
			if ns == "" || name == "" || p.EphemeralStorage == nil || p.EphemeralStorage.UsedBytes == nil {
				continue
			}
			used := int64(*p.EphemeralStorage.UsedBytes)
			if used <= diagnoseLargePodBytes {
				continue
			}
			key := ns + "/" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Issue{
				Severity:     sevWarning,
				BaseSeverity: sevWarning,
				Category:     "storage",
				Kind:         "Pod",
				Namespace:    ns,
				Name:         name,
				Reason:       "LargeEphemeral",
				Message:      fmt.Sprintf("非持久化存储占用 %s，超过 5Gi", fmtBytes(float64(used))),
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
	// Stable order so issue-cap truncation is deterministic across runs.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
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
	if used, cap, ok := nfsUsageFromSummaries(ctx, c, summaries, pods, pick.Name); ok && cap > 0 {
		res.UsedBytes = used
		res.CapacityBytes = cap
		res.Percent = float64(used) / float64(cap) * 100
		res.Probe = "summary"
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
	if used, cap, ok := c.nfsUsageViaExecDF(ctx, pods, pick.Name); ok && cap > 0 {
		res.UsedBytes = used
		res.CapacityBytes = cap
		res.Percent = float64(used) / float64(cap) * 100
		res.Probe = "exec-df"
		if res.Percent/100 >= diagnoseNFSCriticalRatio {
			res.Issue = nfsHighUsageIssue(pick, res)
		}
		return res
	}

	if ctx.Err() != nil {
		res.Skipped = "timeout budget exhausted"
		return res
	}
	res.Skipped = "no mounted PVC of target SC"
	return res
}

func nfsHighUsageIssue(pick nfsSCPick, res nfsUsageResult) *Issue {
	it := Issue{
		Severity:     sevCritical,
		BaseSeverity: sevCritical,
		Category:     "storage",
		Kind:         "StorageClass",
		Name:         pick.Name,
		Reason:       "NFSHighUsage",
		Message: fmt.Sprintf("NFS 使用率 %.0f%%（%s / %s），超过 80%%",
			res.Percent, fmtBytes(float64(res.UsedBytes)), fmtBytes(float64(res.CapacityBytes))),
		Evidence: clipEvidence([]string{
			fmt.Sprintf("usage=%.1f%%", res.Percent),
			fmt.Sprintf("used=%s", fmtBytes(float64(res.UsedBytes))),
			fmt.Sprintf("capacity=%s", fmtBytes(float64(res.CapacityBytes))),
			"threshold=80%",
			"scSource=" + pick.Source,
			"probe=" + res.Probe,
			"provisioner=" + pick.SC.Provisioner,
		}),
	}
	return &it
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
	return p != "" && strings.Contains(p, "nfs")
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

func nfsUsageFromSummaries(ctx context.Context, c *Clients, summaries map[string]nodeStatsSummary, pods []corev1.Pod, scName string) (used, capacity int64, ok bool) {
	samples := volumeSamplesFromSummaries(summaries)
	if len(samples) > 0 {
		refs := make([]pvcRef, 0, len(samples))
		byKey := map[string]volSample{}
		for _, s := range samples {
			refs = append(refs, s.Ref)
			byKey[s.Ref.Namespace+"/"+s.Ref.Name] = s
		}
		matched := filterPVCsBySC(ctx, c, refs, scName, 1)
		if len(matched) > 0 {
			s := byKey[matched[0].Namespace+"/"+matched[0].Name]
			return s.Used, s.Cap, true
		}
	}

	// No volume stats for target SC yet: resolve a few claims from Running pods, then re-check samples.
	claims := filterPVCsBySC(ctx, c, pvcRefsFromPods(pods), scName, diagnoseNFSPVCMatchMax)
	if len(claims) == 0 {
		return 0, 0, false
	}
	want := map[string]bool{}
	for _, ref := range claims {
		want[ref.Namespace+"/"+ref.Name] = true
	}
	for _, s := range samples {
		if want[s.Ref.Namespace+"/"+s.Ref.Name] {
			return s.Used, s.Cap, true
		}
	}
	return 0, 0, false
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

func (c *Clients) nfsUsageViaExecDF(ctx context.Context, pods []corev1.Pod, scName string) (used, capacity int64, ok bool) {
	claims := filterPVCsBySC(ctx, c, pvcRefsFromPods(pods), scName, diagnoseNFSPVCMatchMax)
	if len(claims) == 0 {
		return 0, 0, false
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
		u, cap, parsed := parseDFPOutput(stdout.String())
		if !parsed || cap <= 0 {
			continue
		}
		return u, cap, true
	}
	return 0, 0, false
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

// parseDFPOutput parses `df -P <path>` (1024-block POSIX). Returns used/capacity bytes.
func parseDFPOutput(out string) (used, capacity int64, ok bool) {
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
		return usedBlocks * 1024, blocks * 1024, true
	}
	return 0, 0, false
}
