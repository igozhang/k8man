package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

const (
	// NewWindow is how long an error+ issue counts as "newly appeared".
	NewWindow = 2 * time.Hour
	maxLines  = 12
	// NotifyTimeout caps one async push (firing plus recovery) so a slow Feishu cannot run forever.
	NotifyTimeout = 90 * time.Second
	maxWebhookURL = 512
)

// Notifier pushes diagnose issues to enabled Feishu webhooks.
type Notifier struct {
	Store  *store.Store
	Client *http.Client
	now    func() time.Time
	// OnFired runs after a new or escalated alert is delivered. It must return quickly
	// and must not call Notify.
	OnFired func(clusterID string)

	sendMu    sync.Mutex
	sendLocks map[string]*sync.Mutex

	roundMu sync.Mutex
	rounds  map[string]int
}

func New(st *store.Store) *Notifier {
	return &Notifier{
		Store: st,
		Client: &http.Client{
			Timeout: 12 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

// CanonicalFingerprint folds a stored key the same way IssueFingerprint does.
func CanonicalFingerprint(fp string) string {
	return canonicalFingerprint(fp)
}

// canonicalReason folds reasons that are one incident with two labels.
// Only ErrImagePull and ImagePullBackOff: the kubelet alternates them while the pull is still failing.
func canonicalReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "ErrImagePull", "ImagePullBackOff":
		return "ImagePull"
	default:
		return strings.TrimSpace(reason)
	}
}

// IssueFingerprint identifies an issue across scans (severity excluded so decay does not re-fire).
func IssueFingerprint(it kube.Issue) string {
	return strings.Join([]string{
		strings.TrimSpace(it.Category),
		strings.TrimSpace(it.Kind),
		strings.TrimSpace(it.Namespace),
		strings.TrimSpace(it.Name),
		canonicalReason(it.Reason),
	}, "|")
}

// canonicalFingerprint maps a stored fingerprint onto IssueFingerprint, including rows saved
// before equivalent reasons were folded together.
func canonicalFingerprint(fp string) string {
	parts := strings.Split(fp, "|")
	if len(parts) < 5 {
		return fp
	}
	reason := canonicalReason(strings.Join(parts[4:], "|"))
	return strings.Join(append(parts[:4], reason), "|")
}

func fingerprintOK(fp string) bool {
	return fp != "" && fp != "||||"
}

// IsAlertable reports whether an issue matches push rules:
// - critical always
// - error when first seen within NewWindow
func IsAlertable(it kube.Issue, now time.Time) bool {
	sev := strings.ToLower(strings.TrimSpace(it.Severity))
	if sev == "critical" {
		return true
	}
	if sev != "error" {
		return false
	}
	since, ok := parseSinceAt(it.SinceAt)
	if !ok {
		// No sinceAt: treat as new so we do not miss error-level items.
		return true
	}
	return !since.After(now) && now.Sub(since) <= NewWindow
}

func parseSinceAt(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ValidateWebhookURL restricts targets to Feishu/Lark bot hooks (SSRF / typo guard).
func ValidateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("url is required")
	}
	if len(raw) > maxWebhookURL {
		return fmt.Errorf("url too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("url must use https")
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "open.feishu.cn", "open.larksuite.com":
		// ok
	default:
		return fmt.Errorf("webhook host must be open.feishu.cn or open.larksuite.com")
	}
	if !strings.Contains(u.Path, "/open-apis/bot/") {
		return fmt.Errorf("url must be a Feishu/Lark bot webhook path")
	}
	return nil
}

// RecoverGate says whether this round may close previously sent alerts.
// OK on a full diagnose requires a successful probe and a complete diagnose
// (not truncated, no category errors). HoldNFS / HoldLargePod keep those
// fingerprints open when that scan was skipped.
// Scope, when non-nil, is the only set of canonical fingerprints this round
// may close. Nil means a complete diagnose. An empty map closes nothing.
type RecoverGate struct {
	OK           bool
	HoldNFS      bool
	HoldLargePod bool
	Scope        map[string]struct{}
}

// RecoverGateFor is closed unless probe succeeded and diagnose finished without truncation or category errors.
func RecoverGateFor(probeStatus string, diag *kube.DiagnoseReport) RecoverGate {
	if !strings.EqualFold(strings.TrimSpace(probeStatus), "ok") || diag == nil || diag.Truncated || len(diag.Errors) > 0 {
		return RecoverGate{}
	}
	return RecoverGate{
		OK:           true,
		HoldNFS:      strings.TrimSpace(diag.Scan.NFSSkipped) != "",
		HoldLargePod: strings.TrimSpace(diag.Scan.LargePodSkipped) != "",
	}
}

// NextRound marks a new inspect result for this cluster. An older round still
// waiting on a webhook must not send after this returns.
func (n *Notifier) NextRound(clusterID string) int {
	if n == nil || clusterID == "" {
		return 0
	}
	n.roundMu.Lock()
	defer n.roundMu.Unlock()
	if n.rounds == nil {
		n.rounds = map[string]int{}
	}
	n.rounds[clusterID]++
	return n.rounds[clusterID]
}

func (n *Notifier) latestRound(clusterID string) int {
	n.roundMu.Lock()
	defer n.roundMu.Unlock()
	return n.rounds[clusterID]
}

// CurrentRound is the newest inspect generation for this cluster.
// A recovery recheck uses it without incrementing, so it does not discard
// a full diagnose that is still waiting to push.
func (n *Notifier) CurrentRound(clusterID string) int {
	if n == nil || clusterID == "" {
		return 0
	}
	return n.latestRound(clusterID)
}

// Notify delivers firing and recovery notices and returns after webhook attempts finish.
// round 0 skips the stale-round check (tests). A positive round sends only if it is still the latest.
func (n *Notifier) Notify(cluster *model.Cluster, issues []kube.Issue, gate RecoverGate, round int) {
	if n == nil || n.Store == nil || cluster == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), NotifyTimeout)
	defer cancel()
	n.notifyCluster(ctx, cluster, issues, gate, round)
}

// NotifyAsync pushes firing and recovery notices without blocking the inspect round.
func (n *Notifier) NotifyAsync(cluster *model.Cluster, issues []kube.Issue, gate RecoverGate, round int) {
	if n == nil || n.Store == nil || cluster == nil {
		return
	}
	cp := append([]kube.Issue(nil), issues...)
	cl := *cluster
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("alert panic %s: %v", cl.ID, r)
			}
		}()
		n.Notify(&cl, cp, gate, round)
	}()
}

func (n *Notifier) notifyCluster(ctx context.Context, cluster *model.Cluster, issues []kube.Issue, gate RecoverGate, round int) {
	// One cluster at a time. Async patrol pushes and recovery probes would otherwise
	// both read alert_sent before either upserts, and send the same alert twice.
	unlock := n.lockCluster(cluster.ID)
	defer unlock()
	if round > 0 && round != n.latestRound(cluster.ID) {
		return
	}
	now := n.now()
	if now.IsZero() {
		now = time.Now()
	}
	sent, err := n.Store.ListAlertSent(cluster.ID)
	if err != nil {
		log.Printf("alert list-sent %s: %v", cluster.ID, err)
		return
	}
	sent = n.alignOpenSent(cluster.ID, issues, sent, now)
	pending, recovered := classifyAlerts(issues, sent, now, gate)
	if len(pending) == 0 && len(recovered) == 0 {
		return
	}
	hooks, err := n.Store.ListEnabledAlertWebhooks()
	if err != nil {
		log.Printf("alert list-hooks: %v", err)
		return
	}
	if len(hooks) == 0 {
		return
	}

	fired := 0
	if len(pending) > 0 {
		if n.postHooks(ctx, cluster.ID, hooks, FormatMessage(cluster.Name, pending, now)) {
			for _, it := range pending {
				fp := IssueFingerprint(it)
				if err := n.Store.UpsertAlertSent(cluster.ID, fp, it.Severity, formatIssueBlock(1, it), now); err != nil {
					log.Printf("alert mark-sent %s: %v", cluster.ID, err)
					continue
				}
				fired++
			}
		}
	}
	if fired > 0 && n.OnFired != nil {
		n.OnFired(cluster.ID)
	}
	if ctx.Err() != nil {
		log.Printf("alert aborted %s: %v", cluster.ID, ctx.Err())
		return
	}
	if len(recovered) == 0 {
		return
	}
	blocks := make([]string, len(recovered))
	for i, row := range recovered {
		blocks[i] = strings.TrimSpace(row.Detail)
		if blocks[i] == "" {
			blocks[i] = detailFromFingerprint(row.Fingerprint, row.Severity)
		}
	}
	if n.postHooks(ctx, cluster.ID, hooks, FormatRecovery(cluster.Name, blocks, now)) {
		if err := n.Store.DeleteAlertSent(cluster.ID, fingerprintsToClear(sent, recovered)); err != nil {
			log.Printf("alert clear-sent %s: %v", cluster.ID, err)
		}
	}
}

// alignOpenSent rewrites legacy fingerprints onto the current incident key without notifying.
// A pull that flips between ErrImagePull and ImagePullBackOff stays one open alert.
func (n *Notifier) alignOpenSent(clusterID string, issues []kube.Issue, sent map[string]store.AlertSentRow, now time.Time) map[string]store.AlertSentRow {
	if n == nil || n.Store == nil || len(sent) == 0 {
		return sent
	}
	current := map[string]kube.Issue{}
	for _, it := range issues {
		fp := IssueFingerprint(it)
		if fingerprintOK(fp) {
			current[fp] = it
		}
	}
	type move struct {
		from []string
		sev  string
		it   kube.Issue
	}
	groups := map[string]*move{}
	for fp, row := range sent {
		canon := canonicalFingerprint(fp)
		it, ok := current[canon]
		if !ok || canon == fp {
			continue
		}
		g := groups[canon]
		if g == nil {
			g = &move{sev: row.Severity, it: it}
			groups[canon] = g
		}
		g.from = append(g.from, fp)
		if strings.EqualFold(row.Severity, "critical") {
			g.sev = "critical"
		}
	}
	if len(groups) == 0 {
		return sent
	}
	out := make(map[string]store.AlertSentRow, len(sent))
	for fp, row := range sent {
		out[fp] = row
	}
	for canon, g := range groups {
		detail := formatIssueBlock(1, g.it)
		if err := n.Store.UpsertAlertSent(clusterID, canon, g.sev, detail, now); err != nil {
			log.Printf("alert align %s: %v", clusterID, err)
			continue
		}
		if err := n.Store.DeleteAlertSent(clusterID, g.from); err != nil {
			log.Printf("alert align clear %s: %v", clusterID, err)
			continue
		}
		for _, fp := range g.from {
			delete(out, fp)
		}
		out[canon] = store.AlertSentRow{Fingerprint: canon, Severity: g.sev, Detail: detail}
	}
	return out
}

func fingerprintsToClear(sent map[string]store.AlertSentRow, recovered []store.AlertSentRow) []string {
	want := map[string]struct{}{}
	for _, row := range recovered {
		want[canonicalFingerprint(row.Fingerprint)] = struct{}{}
	}
	fps := make([]string, 0, len(want))
	for fp := range sent {
		if _, ok := want[canonicalFingerprint(fp)]; ok {
			fps = append(fps, fp)
		}
	}
	sort.Strings(fps)
	return fps
}

func (n *Notifier) lockCluster(id string) func() {
	n.sendMu.Lock()
	if n.sendLocks == nil {
		n.sendLocks = map[string]*sync.Mutex{}
	}
	mu := n.sendLocks[id]
	if mu == nil {
		mu = &sync.Mutex{}
		n.sendLocks[id] = mu
	}
	n.sendMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (n *Notifier) postHooks(ctx context.Context, clusterID string, hooks []model.AlertWebhook, text string) bool {
	okAny := false
	for _, h := range hooks {
		if ctx.Err() != nil {
			log.Printf("alert aborted %s: %v", clusterID, ctx.Err())
			break
		}
		if err := ValidateWebhookURL(h.URL); err != nil {
			log.Printf("alert skip hook %s (%s): %v", h.Name, h.ID, err)
			continue
		}
		if err := n.PostFeishu(ctx, h.URL, text); err != nil {
			log.Printf("alert feishu %s (%s): %v", h.Name, h.ID, err)
			continue
		}
		okAny = true
	}
	return okAny
}

func classifyAlerts(issues []kube.Issue, sent map[string]store.AlertSentRow, now time.Time, gate RecoverGate) (pending []kube.Issue, recovered []store.AlertSentRow) {
	current := map[string]struct{}{}
	pendingFP := map[string]struct{}{}
	byCanon := map[string][]store.AlertSentRow{}
	for fp, row := range sent {
		if row.Fingerprint == "" {
			row.Fingerprint = fp
		}
		byCanon[canonicalFingerprint(fp)] = append(byCanon[canonicalFingerprint(fp)], row)
	}
	for _, it := range issues {
		fp := IssueFingerprint(it)
		if !fingerprintOK(fp) {
			continue
		}
		current[fp] = struct{}{}
		if !IsAlertable(it, now) {
			continue
		}
		if _, ok := pendingFP[fp]; ok {
			continue
		}
		if prevs := byCanon[fp]; len(prevs) > 0 {
			// Re-alert only when severity escalates to critical.
			if !(strings.EqualFold(it.Severity, "critical") && !groupHasCritical(prevs)) {
				continue
			}
		}
		pendingFP[fp] = struct{}{}
		pending = append(pending, it)
	}
	if !gate.OK {
		return pending, nil
	}
	fps := make([]string, 0, len(sent))
	for fp := range sent {
		if _, ok := current[canonicalFingerprint(fp)]; ok {
			continue
		}
		if !allowRecover(fp, gate) {
			continue
		}
		fps = append(fps, fp)
	}
	sort.Slice(fps, func(i, j int) bool {
		ri, rj := sevRank(sent[fps[i]].Severity), sevRank(sent[fps[j]].Severity)
		if ri != rj {
			return ri < rj
		}
		return fps[i] < fps[j]
	})
	seen := map[string]struct{}{}
	for _, fp := range fps {
		canon := canonicalFingerprint(fp)
		if _, ok := seen[canon]; ok {
			continue
		}
		seen[canon] = struct{}{}
		row := sent[fp]
		if row.Fingerprint == "" {
			row.Fingerprint = fp
		}
		recovered = append(recovered, row)
	}
	return pending, recovered
}

func groupHasCritical(rows []store.AlertSentRow) bool {
	for _, row := range rows {
		if strings.EqualFold(row.Severity, "critical") {
			return true
		}
	}
	return false
}

func allowRecover(fp string, gate RecoverGate) bool {
	if !gate.OK {
		return false
	}
	if gate.Scope != nil {
		if _, ok := gate.Scope[canonicalFingerprint(fp)]; !ok {
			return false
		}
	}
	reason := fingerprintReason(fp)
	if gate.HoldNFS && reason == "NFSHighUsage" {
		return false
	}
	if gate.HoldLargePod && reason == "LargeEphemeral" {
		return false
	}
	return true
}

func fingerprintReason(fp string) string {
	parts := strings.Split(fp, "|")
	if len(parts) < 5 {
		return ""
	}
	return strings.Join(parts[4:], "|")
}

func sevRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return 0
	case "error":
		return 1
	case "warning":
		return 2
	default:
		return 3
	}
}

func detailFromFingerprint(fp, severity string) string {
	parts := strings.Split(fp, "|")
	it := kube.Issue{Severity: severity}
	if len(parts) >= 5 {
		it.Category = parts[0]
		it.Kind = parts[1]
		it.Namespace = parts[2]
		it.Name = parts[3]
		it.Reason = strings.Join(parts[4:], "|")
	} else {
		it.Name = fp
	}
	return formatIssueBlock(1, it)
}

// FormatMessage builds a plain-text Feishu body.
func FormatMessage(clusterName string, issues []kube.Issue, now time.Time) string {
	var b strings.Builder
	b.WriteString("【k8man 告警】\n")
	b.WriteString("集群: ")
	if strings.TrimSpace(clusterName) == "" {
		b.WriteString("(未命名)")
	} else {
		b.WriteString(clusterName)
	}
	b.WriteString("\n")
	b.WriteString("时间: ")
	b.WriteString(now.Local().Format("2006-01-02 15:04:05"))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("共 %d 条需关注:\n", len(issues)))
	limit := len(issues)
	if limit > maxLines {
		limit = maxLines
	}
	for i := 0; i < limit; i++ {
		b.WriteString(formatIssueBlock(i+1, issues[i]))
	}
	if len(issues) > maxLines {
		b.WriteString(fmt.Sprintf("…另有 %d 条未列出\n", len(issues)-maxLines))
	}
	return b.String()
}

// FormatRecovery builds the plain-text body for issues that disappeared after a complete diagnose.
// blocks are stored alert bodies (already numbered); they are renumbered here.
func FormatRecovery(clusterName string, blocks []string, now time.Time) string {
	var b strings.Builder
	b.WriteString("【k8man 恢复】\n")
	b.WriteString("集群: ")
	if strings.TrimSpace(clusterName) == "" {
		b.WriteString("(未命名)")
	} else {
		b.WriteString(clusterName)
	}
	b.WriteString("\n")
	b.WriteString("时间: ")
	b.WriteString(now.Local().Format("2006-01-02 15:04:05"))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("以下 %d 条已恢复:\n", len(blocks)))
	limit := len(blocks)
	if limit > maxLines {
		limit = maxLines
	}
	for i := 0; i < limit; i++ {
		b.WriteString(renumberBlock(i+1, blocks[i]))
		b.WriteString("\n")
	}
	if len(blocks) > maxLines {
		b.WriteString(fmt.Sprintf("…另有 %d 条未列出\n", len(blocks)-maxLines))
	}
	return b.String()
}

func renumberBlock(n int, block string) string {
	block = strings.TrimRight(block, "\n")
	if i := strings.IndexByte(block, '.'); i > 0 && i < 6 && isDigits(block[:i]) {
		return fmt.Sprintf("%d.%s", n, block[i+1:])
	}
	return fmt.Sprintf("%d. %s", n, block)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func formatIssueBlock(n int, it kube.Issue) string {
	ctrl := controllerLabel(it.Kind)
	where := it.Name
	if ns := strings.TrimSpace(it.Namespace); ns != "" {
		where = ns + "/" + it.Name
	}
	var b strings.Builder
	// 1. [严重] sts ns/name
	b.WriteString(fmt.Sprintf("%d. [%s] %s %s\n", n, severityCN(it.Severity), ctrl, where))

	// 原因 + 说明 + 持续
	var mid []string
	if r := strings.TrimSpace(it.Reason); r != "" {
		mid = append(mid, r)
	}
	if m := humanizeIssueMessage(it); m != "" {
		mid = append(mid, m)
	}
	if s := strings.TrimSpace(it.Since); s != "" {
		mid = append(mid, "已持续 "+s)
	}
	if len(mid) > 0 {
		b.WriteString("   ")
		b.WriteString(strings.Join(mid, " · "))
		b.WriteString("\n")
	}

	// 1–2 条最重要相关信息（副本态 / 节点 / 条件 / 容器等待）
	for _, line := range pickAlertDetails(it, 2) {
		b.WriteString("   ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// controllerLabel shortens Kind for Feishu (deploy / sts / ds …).
func controllerLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case "Deployment":
		return "deploy"
	case "StatefulSet":
		return "sts"
	case "DaemonSet":
		return "ds"
	case "ReplicaSet":
		return "rs"
	case "Pod":
		return "pod"
	case "Node":
		return "node"
	case "StorageClass":
		return "sc"
	case "":
		return "obj"
	default:
		return kind
	}
}

func humanizeIssueMessage(it kube.Issue) string {
	msg := strings.TrimSpace(kube.EnrichIssueMessage(it))
	if msg == "" {
		return ""
	}
	reason := strings.TrimSpace(it.Reason)
	if msg == reason {
		return ""
	}
	switch reason {
	case "NotReady", "Misscheduled":
		msg = strings.Replace(msg, "ready ", "Ready ", 1)
		msg = strings.ReplaceAll(msg, "misscheduled ", "错位调度 ")
	}
	msg = strings.ReplaceAll(msg, " ago", " 前")
	limit := 120 // room for · node (ip) suffix
	if it.Reason == "NFSHighUsage" || it.Category == "storage" {
		limit = 180 // server:/path + usage
	}
	return clip(msg, limit)
}

func pickAlertDetails(it kube.Issue, limit int) []string {
	if limit <= 0 {
		return nil
	}
	msg := strings.ToLower(strings.TrimSpace(it.Message))
	reason := strings.ToLower(strings.TrimSpace(it.Reason))
	var out []string
	seen := map[string]struct{}{}
	add := func(raw string) {
		s := humanizeEvidenceLine(raw)
		if s == "" {
			return
		}
		low := strings.ToLower(s)
		if low == msg || low == reason {
			return
		}
		// 与主说明高度重复（如 ready 0/1）则跳过
		if msg != "" && strings.Contains(low, msg) {
			return
		}
		if _, ok := seen[low]; ok {
			return
		}
		seen[low] = struct{}{}
		out = append(out, clip(s, 120))
	}

	// 节点类：优先节点与用量/条件
	if it.Category == "node" || it.Kind == "Node" {
		if it.NodeIP != "" {
			add("ip=" + it.NodeIP)
		}
	}
	// 存储 · NFS：服务器 IP/主机名必须进告警（Message 经 EnrichIssueMessage 补全，证据再兜底）
	if it.Reason == "NFSHighUsage" || it.Kind == "StorageClass" {
		if s := kube.IssueNFSServer(it); s != "" {
			add("server=" + s)
		}
		if p := kube.IssueNFSPath(it); p != "" {
			add("path=" + p)
		}
	}
	// Pod（含 LargeEphemeral category=storage）：节点位置
	if (it.Category == "pod" || it.Kind == "Pod") && (it.Node != "" || it.NodeIP != "") {
		var parts []string
		if it.Node != "" {
			parts = append(parts, it.Node)
		}
		if it.NodeIP != "" {
			parts = append(parts, "("+it.NodeIP+")")
		}
		add("节点 " + strings.Join(parts, " "))
	}
	if it.Container != "" {
		add("容器 " + it.Container)
	}

	// evidence：按重要度挑
	type scored struct {
		score int
		line  string
	}
	var ranked []scored
	for _, e := range it.Evidence {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.HasPrefix(e, "severityNote:") {
			continue
		}
		if strings.HasPrefix(e, "Warning ") {
			continue // 事件挂靠噪音大，告警里不优先
		}
		sc := scoreEvidenceLine(e, it)
		if sc <= 0 {
			continue
		}
		ranked = append(ranked, scored{score: sc, line: e})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})
	for _, r := range ranked {
		if len(out) >= limit {
			break
		}
		add(r.line)
	}
	// 全局兜底：结构化字段有定位信息但尚未写入细节行时补一行
	if len(out) < limit {
		for _, line := range kube.IssueLocationLines(it) {
			if len(out) >= limit {
				break
			}
			add(line)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func scoreEvidenceLine(line string, it kube.Issue) int {
	low := strings.ToLower(line)
	switch {
	// 定位关键信息优先：NFS 服务器、导出路径、副本态等
	case strings.HasPrefix(low, "server=") || strings.HasPrefix(low, "nfsserver=") ||
		strings.HasPrefix(low, "host=") || strings.HasPrefix(low, "addr="):
		return 110
	case strings.HasPrefix(low, "path=") || strings.HasPrefix(low, "share=") || strings.HasPrefix(low, "export="):
		return 105
	case strings.HasPrefix(low, "replicas ") || strings.HasPrefix(low, "desired="):
		return 100
	case strings.Contains(low, " waiting "):
		return 90
	case strings.Contains(low, "lastexit") || strings.Contains(low, "oomkilled"):
		return 85
	case strings.Contains(low, "=false"):
		return 70
	case strings.HasPrefix(low, "phase="):
		return 60
	case strings.HasPrefix(low, "sc=") || strings.HasPrefix(low, "provisioner="):
		return 58
	case strings.HasPrefix(low, "node=") || strings.HasPrefix(low, "ip="):
		if it.Category == "pod" {
			return 40 // 已在上面单独写节点时可能重复，分低些
		}
		return 55
	case strings.HasPrefix(low, "podip="):
		return 20
	default:
		return 30
	}
}

func humanizeEvidenceLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, " ago", " 前")
	s = strings.ReplaceAll(s, "misscheduled=", "错位调度=")
	if strings.HasPrefix(s, "replicas ") {
		s = "副本 " + strings.TrimPrefix(s, "replicas ")
	}
	if strings.HasPrefix(s, "desired=") {
		s = "调度 " + s
	}
	if strings.HasPrefix(s, "server=") {
		s = "NFS服务器 " + strings.TrimPrefix(s, "server=")
	}
	if strings.HasPrefix(s, "nfsServer=") {
		s = "NFS服务器 " + strings.TrimPrefix(s, "nfsServer=")
	}
	if strings.HasPrefix(s, "host=") {
		s = "NFS服务器 " + strings.TrimPrefix(s, "host=")
	}
	if strings.HasPrefix(s, "addr=") {
		s = "NFS服务器 " + strings.TrimPrefix(s, "addr=")
	}
	if strings.HasPrefix(s, "path=") {
		s = "导出路径 " + strings.TrimPrefix(s, "path=")
	}
	if strings.HasPrefix(s, "share=") {
		s = "共享 " + strings.TrimPrefix(s, "share=")
	}
	if strings.HasPrefix(s, "sc=") {
		s = "StorageClass " + strings.TrimPrefix(s, "sc=")
	}
	if strings.HasPrefix(s, "ip=") {
		s = "IP " + strings.TrimPrefix(s, "ip=")
	}
	return s
}

func severityCN(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return "严重"
	case "error":
		return "错误"
	case "warning":
		return "警告"
	case "info":
		return "提示"
	default:
		return s
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

type feishuTextPayload struct {
	MsgType string `json:"msg_type"`
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// PostFeishu sends a text message to a Feishu bot webhook URL.
func (n *Notifier) PostFeishu(ctx context.Context, webhookURL, text string) error {
	if n == nil {
		return fmt.Errorf("notifier nil")
	}
	webhookURL = strings.TrimSpace(webhookURL)
	if err := ValidateWebhookURL(webhookURL); err != nil {
		return err
	}
	var body feishuTextPayload
	body.MsgType = "text"
	body.Content.Text = text
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
	}
	// Feishu returns 200 with {"code":0} or {"StatusCode":0}
	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err == nil {
		if code, ok := asInt(parsed["code"]); ok && code != 0 {
			return fmt.Errorf("feishu code %d: %s", code, strings.TrimSpace(string(respBody)))
		}
		if code, ok := asInt(parsed["StatusCode"]); ok && code != 0 {
			return fmt.Errorf("feishu StatusCode %d: %s", code, strings.TrimSpace(string(respBody)))
		}
	}
	return nil
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		i, err := t.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

// TestWebhook posts a short probe message.
func (n *Notifier) TestWebhook(ctx context.Context, webhookURL string) error {
	return n.PostFeishu(ctx, webhookURL, "【k8man 告警】测试消息：Webhook 可用。")
}
