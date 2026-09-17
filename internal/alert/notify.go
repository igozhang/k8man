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
		it := issues[i]
		ns := it.Namespace
		if ns == "" {
			ns = "—"
		}
		b.WriteString(fmt.Sprintf(
			"%d. [%s] %s %s/%s · %s · %s\n",
			i+1,
			severityCN(it.Severity),
			it.Kind,
			ns,
			it.Name,
			it.Reason,
			clip(it.Message, 80),
		))
	}
	if len(issues) > maxLines {
		b.WriteString(fmt.Sprintf("…另有 %d 条未列出\n", len(issues)-maxLines))
	}
	return b.String()
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
