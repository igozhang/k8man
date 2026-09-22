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
	"time"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

const (
	// NewWindow is how long an error+ issue counts as "newly appeared".
	NewWindow = 2 * time.Hour
	maxLines  = 12
	// NotifyTimeout caps total push work so a slow Feishu never blocks inspect forever.
	NotifyTimeout = 45 * time.Second
	maxWebhookURL = 512
)

// Notifier pushes diagnose issues to enabled Feishu webhooks.
type Notifier struct {
	Store  *store.Store
	Client *http.Client
	now    func() time.Time
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

// IssueFingerprint identifies an issue across scans (severity excluded so decay does not re-fire).
func IssueFingerprint(it kube.Issue) string {
	return strings.Join([]string{
		strings.TrimSpace(it.Category),
		strings.TrimSpace(it.Kind),
		strings.TrimSpace(it.Namespace),
		strings.TrimSpace(it.Name),
		strings.TrimSpace(it.Reason),
	}, "|")
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

// NotifyCluster filters issues, dedupes via alert_sent, and posts to enabled webhooks.
// Uses its own timeout so a cancelled inspect context or slow Feishu cannot stall the caller.
func (n *Notifier) NotifyCluster(_ context.Context, cluster *model.Cluster, issues []kube.Issue) {
	if n == nil || n.Store == nil || cluster == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), NotifyTimeout)
	defer cancel()
	n.notifyCluster(ctx, cluster, issues)
}

func (n *Notifier) notifyCluster(ctx context.Context, cluster *model.Cluster, issues []kube.Issue) {
	now := n.now()
	if now.IsZero() {
		now = time.Now()
	}

	keep := make([]string, 0, len(issues))
	seenFP := map[string]struct{}{}
	for _, it := range issues {
		fp := IssueFingerprint(it)
		if !fingerprintOK(fp) {
			continue
		}
		if _, ok := seenFP[fp]; ok {
			continue
		}
		seenFP[fp] = struct{}{}
		keep = append(keep, fp)
	}
	if err := n.Store.PruneAlertSent(cluster.ID, keep); err != nil {
		log.Printf("alert prune %s: %v", cluster.ID, err)
	}

	sent, err := n.Store.ListAlertSentFingerprints(cluster.ID)
	if err != nil {
		log.Printf("alert list-sent %s: %v", cluster.ID, err)
		return
	}

	var pending []kube.Issue
	pendingFP := map[string]struct{}{}
	for _, it := range issues {
		if !IsAlertable(it, now) {
			continue
		}
		fp := IssueFingerprint(it)
		if !fingerprintOK(fp) {
			continue
		}
		if _, ok := pendingFP[fp]; ok {
			continue
		}
		if prev, ok := sent[fp]; ok {
			// Re-alert only when severity escalates to critical.
			if !(strings.EqualFold(it.Severity, "critical") && !strings.EqualFold(prev, "critical")) {
				continue
			}
		}
		pendingFP[fp] = struct{}{}
		pending = append(pending, it)
	}
	if len(pending) == 0 {
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

	text := FormatMessage(cluster.Name, pending, now)
	okAny := false
	for _, h := range hooks {
		if ctx.Err() != nil {
			log.Printf("alert aborted %s: %v", cluster.ID, ctx.Err())
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
	if !okAny {
		return
	}
	for _, it := range pending {
		fp := IssueFingerprint(it)
		if err := n.Store.UpsertAlertSent(cluster.ID, fp, it.Severity, now); err != nil {
			log.Printf("alert mark-sent %s: %v", cluster.ID, err)
		}
	}
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
