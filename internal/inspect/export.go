package inspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/store"
)

// DocCategoryInspectionReport is the ops-doc category for synced inspection exports.
const DocCategoryInspectionReport = "inspection-report"

const (
	maxExportTitleRunes  = 180
	maxIssuesPerCluster  = 500
	maxIssueMessageRunes = 400
)

// ExportPeriod is day | week | month.
type ExportPeriod string

const (
	ExportPeriodDay   ExportPeriod = "day"
	ExportPeriodWeek  ExportPeriod = "week"
	ExportPeriodMonth ExportPeriod = "month"
)

// ExportScope is current | all.
type ExportScope string

const (
	ExportScopeCurrent ExportScope = "current"
	ExportScopeAll     ExportScope = "all"
)

var (
	// ErrExportClusterIDRequired is returned when scope=current without clusterId.
	ErrExportClusterIDRequired = errors.New("clusterId required for scope=current")
	// ErrExportClusterNotFound is returned when scope=current and cluster is missing.
	ErrExportClusterNotFound = errors.New("cluster not found")
)

// ExportRequest selects which latest snapshots to include and how to label the window.
type ExportRequest struct {
	Period        ExportPeriod
	Scope         ExportScope
	ClusterID     string // required when Scope == current
	Now           time.Time
	Location      *time.Location
	WindowFrom    time.Time // both Window* set → override PeriodWindow
	WindowTo      time.Time
	TitleAt       time.Time // if set, title/filename use this calendar date (not the clock)
	ExtraNote     string
	KeepAllIssues bool // daily archive: do not drop issues whose sinceAt is outside the label window
}

// ExportReport is markdown + metadata for preview / download / docs sync.
type ExportReport struct {
	Title      string    `json:"title"`
	Filename   string    `json:"filename"`
	Summary    string    `json:"summary"`
	Markdown   string    `json:"markdown"`
	Period     string    `json:"period"`
	Scope      string    `json:"scope"`
	WindowFrom time.Time `json:"windowFrom"`
	WindowTo   time.Time `json:"windowTo"`
	ClusterIDs []string  `json:"clusterIds"`
}

var beijingLoc = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}()

// ParseExportPeriod defaults to week.
func ParseExportPeriod(s string) ExportPeriod {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "day", "d", "日":
		return ExportPeriodDay
	case "month", "月":
		return ExportPeriodMonth
	case "week", "w", "周", "":
		return ExportPeriodWeek
	default:
		return ExportPeriodWeek
	}
}

// ParseExportScope defaults to all.
func ParseExportScope(s string) ExportScope {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "current", "cur", "当前", "当前基地":
		return ExportScopeCurrent
	default:
		return ExportScopeAll
	}
}

func periodLabel(p ExportPeriod) string {
	switch p {
	case ExportPeriodDay:
		return "日"
	case ExportPeriodMonth:
		return "月"
	default:
		return "周"
	}
}

func scopeLabel(s ExportScope) string {
	if s == ExportScopeCurrent {
		return "当前基地"
	}
	return "所有基地"
}

func severityLabelCN(sev string) string {
	switch sev {
	case "critical":
		return "严重"
	case "error":
		return "错误"
	case "warning":
		return "警告"
	case "info":
		return "提示"
	default:
		return sev
	}
}

// PeriodWindow returns inclusive [from, to] in loc for the given period ending at now.
func PeriodWindow(period ExportPeriod, now time.Time, loc *time.Location) (from, to time.Time) {
	if loc == nil {
		loc = beijingLoc
	}
	now = now.In(loc)
	to = now
	y, m, d := now.Date()
	switch period {
	case ExportPeriodDay:
		from = time.Date(y, m, d, 0, 0, 0, 0, loc)
	case ExportPeriodMonth:
		from = time.Date(y, m, 1, 0, 0, 0, 0, loc)
	default: // week: Monday 00:00
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		from = time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, -(weekday - 1))
	}
	return from, to
}

func parseSinceAt(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, raw)
	}
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func issueInWindow(it kube.Issue, from, to time.Time) bool {
	t, ok := parseSinceAt(it.SinceAt)
	if !ok {
		// Keep issues without sinceAt so probe/kubeconfig failures still appear.
		return true
	}
	t = t.UTC()
	return !t.Before(from.UTC()) && !t.After(to.UTC())
}

func filterIssues(issues []kube.Issue, from, to time.Time) []kube.Issue {
	if len(issues) == 0 {
		return []kube.Issue{}
	}
	out := make([]kube.Issue, 0, len(issues))
	for _, it := range issues {
		if issueInWindow(it, from, to) {
			out = append(out, it)
		}
	}
	sortIssues(out)
	return out
}

func fmtTimeLocal(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "—"
	}
	if loc == nil {
		loc = beijingLoc
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

// escapeMD flattens text for markdown tables/lists and reduces HTML injection via marked.
func escapeMD(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return strings.TrimSpace(s)
}

func clipRunesMax(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || s == "" || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func issueLine(it kube.Issue) string {
	where := it.Name
	if it.Namespace != "" {
		where = it.Namespace + "/" + it.Name
	}
	parts := []string{
		"**" + severityLabelCN(it.Severity) + "**",
		escapeMD(it.Category),
		escapeMD(where),
	}
	if it.Reason != "" {
		parts = append(parts, "`"+escapeMD(it.Reason)+"`")
	}
	line := "- " + strings.Join(parts, " · ")
	msg := clipRunesMax(escapeMD(it.Message), maxIssueMessageRunes)
	if msg != "" {
		line += " — " + msg
	}
	if it.Since != "" {
		line += "（" + escapeMD(it.Since) + "）"
	}
	return line
}

type exportSection struct {
	item     ListItem
	issues   []kube.Issue
	crit     int
	errN     int
	warn     int
	info     int
	loadNote string
	omitted  int
}

func buildSection(name string, row *store.InspectionRow, from, to time.Time, keepAll bool) exportSection {
	var issues []kube.Issue
	var diag *kube.DiagnoseReport
	loadNote := ""
	useRowCounts := false
	if row != nil {
		raw := strings.TrimSpace(row.ReportJSON)
		if raw == "" {
			loadNote = "巡检记录存在但报告体为空"
			useRowCounts = true
		} else {
			var rep Report
			if err := json.Unmarshal([]byte(raw), &rep); err != nil {
				loadNote = "报告 JSON 解析失败，问题列表不可用（总览计数沿用库内摘要）"
				useRowCounts = true
			} else {
				if rep.Issues == nil {
					rep.Issues = []kube.Issue{}
				}
				issues = rep.Issues
				diag = rep.Diagnose
			}
		}
	}
	item := listItemFromRow(name, row, issues, diag)
	var filtered []kube.Issue
	if keepAll {
		filtered = append([]kube.Issue(nil), issues...)
		sortIssues(filtered)
	} else {
		filtered = filterIssues(issues, from, to)
	}
	crit, errN, warn, info := countIssues(filtered)
	if useRowCounts && row != nil {
		crit, errN, warn, info = row.Critical, row.Error, row.Warning, row.Info
	}
	omitted := 0
	shown := filtered
	if len(shown) > maxIssuesPerCluster {
		omitted = len(shown) - maxIssuesPerCluster
		shown = shown[:maxIssuesPerCluster]
		note := fmt.Sprintf("问题过多，仅列出前 %d 条（另有 %d 条未展开）", maxIssuesPerCluster, omitted)
		if loadNote == "" {
			loadNote = note
		} else {
			loadNote = loadNote + "；" + note
		}
	}
	return exportSection{
		item:     item,
		issues:   shown,
		crit:     crit,
		errN:     errN,
		warn:     warn,
		info:     info,
		loadNote: loadNote,
		omitted:  omitted,
	}
}

func clipTitle(s string) string {
	return clipRunesMax(s, maxExportTitleRunes)
}

// BuildExport assembles a markdown report from latest inspection snapshots (single DB pass).
func (w *Worker) BuildExport(req ExportRequest) (*ExportReport, error) {
	if w == nil || w.Store == nil {
		return nil, fmt.Errorf("inspect worker not configured")
	}
	period := req.Period
	if period == "" {
		period = ExportPeriodWeek
	}
	scope := req.Scope
	if scope == "" {
		scope = ExportScopeAll
	}
	loc := req.Location
	if loc == nil {
		loc = beijingLoc
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	from, to := PeriodWindow(period, now, loc)
	if !req.WindowFrom.IsZero() && !req.WindowTo.IsZero() && !req.WindowTo.Before(req.WindowFrom) {
		from, to = req.WindowFrom.In(loc), req.WindowTo.In(loc)
	}

	wantID := strings.TrimSpace(req.ClusterID)
	if scope == ExportScopeCurrent && wantID == "" {
		return nil, ErrExportClusterIDRequired
	}

	if w.Files != nil {
		fromDay := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, loc)
		toDay := time.Date(to.In(loc).Year(), to.In(loc).Month(), to.In(loc).Day(), 0, 0, 0, 0, loc)
		aggs, err := w.Files.AggregateRange(fromDay, toDay, loc, now)
		if err != nil {
			log.Printf("inspect export files: %v", err)
		} else if len(aggs) > 0 {
			aggs = w.supplementAggFromStore(aggs, req, from, to, loc)
			if scope == ExportScopeCurrent {
				found := false
				for _, a := range aggs {
					if a.ClusterID == wantID {
						found = true
						break
					}
				}
				if !found {
					return nil, ErrExportClusterNotFound
				}
			}
			note := "优先使用 inspection 文件聚合"
			if req.KeepAllIssues {
				note = "日归档：问题指纹并集（含跨轮次）"
			}
			return buildMarkdownFromAgg(req, aggs, from, to, loc, note), nil
		}
	}

	clusters, err := w.Store.ListClusters()
	if err != nil {
		return nil, err
	}
	rows, err := w.Store.ListInspections()
	if err != nil {
		return nil, err
	}
	byID := inspectionMap(rows)

	sections := make([]exportSection, 0, len(clusters))
	for _, c := range clusters {
		if scope == ExportScopeCurrent && c.ID != wantID {
			continue
		}
		var p *store.InspectionRow
		if row, ok := byID[c.ID]; ok {
			p = &row
		}
		sec := buildSection(c.Name, p, from, to, req.KeepAllIssues)
		sec.item.ClusterID = c.ID
		if sec.item.ClusterName == "" {
			sec.item.ClusterName = c.Name
		}
		sections = append(sections, sec)
	}

	if scope == ExportScopeCurrent && len(sections) == 0 {
		return nil, ErrExportClusterNotFound
	}

	scopeText := scopeLabel(scope)
	if scope == ExportScopeCurrent && len(sections) == 1 {
		name := sections[0].item.ClusterName
		if name == "" {
			name = sections[0].item.ClusterID
		}
		scopeText = "当前基地（" + name + "）"
	}
	periodText := periodLabel(period)
	stamp := now.In(loc).Format("2006-01-02 15:04")
	fileStamp := now.In(loc).Format("20060102-1504")
	if !req.TitleAt.IsZero() {
		stamp = req.TitleAt.In(loc).Format("2006-01-02")
		fileStamp = req.TitleAt.In(loc).Format("20060102")
	}
	title := clipTitle(fmt.Sprintf("巡检报告 · %s · %s · %s", periodText, scopeText, stamp))
	filename := fmt.Sprintf(
		"k8man-inspect-%s-%s-%s.md",
		string(period),
		string(scope),
		fileStamp,
	)

	var totalCrit, totalErr, totalWarn, totalInfo, checkedN int
	ids := make([]string, 0, len(sections))
	for _, sec := range sections {
		ids = append(ids, sec.item.ClusterID)
		totalCrit += sec.crit
		totalErr += sec.errN
		totalWarn += sec.warn
		totalInfo += sec.info
		if sec.item.CheckedAt != nil {
			checkedN++
		}
	}
	filterNote := "按 sinceAt 落入窗口筛选，无 sinceAt 保留"
	if req.KeepAllIssues {
		filterNote = "归档时刻最新快照中的全部问题，不按 sinceAt 裁剪"
	}
	summary := fmt.Sprintf(
		"周期=%s；范围=%s；窗口 %s ~ %s；基地 %d（已巡检 %d）；问题 严重%d / 错误%d / 警告%d / 提示%d（%s）",
		periodText, scopeText,
		fmtTimeLocal(from, loc), fmtTimeLocal(to, loc),
		len(sections), checkedN,
		totalCrit, totalErr, totalWarn, totalInfo,
		filterNote,
	)

	var b strings.Builder
	b.Grow(4096)
	b.WriteString("# ")
	b.WriteString(escapeMD(title))
	b.WriteString("\n\n")
	b.WriteString("- 生成时间：")
	b.WriteString(fmtTimeLocal(now, loc))
	b.WriteString("（Asia/Shanghai）\n")
	b.WriteString("- 报告周期：")
	b.WriteString(periodText)
	b.WriteString("\n")
	b.WriteString("- 时间窗口：")
	b.WriteString(fmtTimeLocal(from, loc))
	b.WriteString(" ~ ")
	b.WriteString(fmtTimeLocal(to, loc))
	b.WriteString("\n")
	b.WriteString("- 业务范围：")
	b.WriteString(escapeMD(scopeText))
	b.WriteString("\n")
	b.WriteString("- 数据来源：各基地**最新一轮**巡检快照")
	if req.KeepAllIssues {
		b.WriteString("（保留仍存在的问题，不按 `sinceAt` 裁剪）\n")
	} else {
		b.WriteString("；问题按 `sinceAt` 落入窗口筛选（无 `sinceAt` 的条目保留）\n")
	}
	b.WriteString("- 说明：系统约 2 小时巡检一轮，本报告**不是**日/周/月历史多轮聚合\n")
	if note := strings.TrimSpace(req.ExtraNote); note != "" {
		b.WriteString("- ")
		b.WriteString(escapeMD(note))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## 总览\n\n")
	b.WriteString("| 基地 | 巡检时间 | Probe | 严重 | 错误 | 警告 | 提示 | 摘要 |\n")
	b.WriteString("| --- | --- | --- | ---: | ---: | ---: | ---: | --- |\n")
	for _, sec := range sections {
		name := sec.item.ClusterName
		if name == "" {
			name = sec.item.ClusterID
		}
		checked := "尚未巡检"
		if sec.item.CheckedAt != nil {
			checked = fmtTimeLocal(*sec.item.CheckedAt, loc)
		}
		probe := sec.item.ProbeStatus
		if probe == "" {
			probe = "—"
		}
		sum := sec.item.Summary
		if sec.item.CheckedAt != nil {
			if sec.loadNote != "" && len(sec.issues) == 0 {
				sum = sec.loadNote
			} else {
				sum = makeSummary(kube.ProbeResult{Status: sec.item.ProbeStatus}, sec.crit, sec.errN, sec.warn, sec.info)
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %d | %s |\n",
			escapeMD(name), checked, escapeMD(probe),
			sec.crit, sec.errN, sec.warn, sec.info, escapeMD(sum),
		)
	}
	b.WriteString("\n")

	for _, sec := range sections {
		name := sec.item.ClusterName
		if name == "" {
			name = sec.item.ClusterID
		}
		b.WriteString("## 基地：")
		b.WriteString(escapeMD(name))
		b.WriteString("\n\n")
		if sec.item.CheckedAt == nil {
			b.WriteString("尚未巡检。\n\n")
			continue
		}
		b.WriteString("- 巡检时间：")
		b.WriteString(fmtTimeLocal(*sec.item.CheckedAt, loc))
		b.WriteString("\n")
		b.WriteString("- Probe：")
		b.WriteString(escapeMD(sec.item.ProbeStatus))
		if sec.item.ProbeError != "" {
			b.WriteString("（")
			b.WriteString(escapeMD(sec.item.ProbeError))
			b.WriteString("）")
		}
		b.WriteString("\n")
		b.WriteString("- 窗内问题：")
		fmt.Fprintf(&b, "严重 %d / 错误 %d / 警告 %d / 提示 %d\n", sec.crit, sec.errN, sec.warn, sec.info)
		if sec.loadNote != "" {
			b.WriteString("- 备注：")
			b.WriteString(escapeMD(sec.loadNote))
			b.WriteString("\n")
		}
		b.WriteString("\n")

		if len(sec.issues) == 0 {
			if sec.loadNote == "" {
				b.WriteString("窗内无问题（或本轮无 issues）。\n\n")
			}
			continue
		}
		b.WriteString("### 问题列表\n\n")
		for _, it := range sec.issues {
			b.WriteString(issueLine(it))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	return &ExportReport{
		Title:      title,
		Filename:   filename,
		Summary:    summary,
		Markdown:   b.String(),
		Period:     string(period),
		Scope:      string(scope),
		WindowFrom: from.UTC(),
		WindowTo:   to.UTC(),
		ClusterIDs: ids,
	}, nil
}

// supplementAggFromStore adds clusters present in SQLite but missing from file aggregation,
// only when the latest inspection CheckedAt falls inside [from, to] (avoids leaking later-day data into day archives).
func (w *Worker) supplementAggFromStore(aggs []AggCluster, req ExportRequest, from, to time.Time, loc *time.Location) []AggCluster {
	if w == nil || w.Store == nil {
		return aggs
	}
	clusters, err := w.Store.ListClusters()
	if err != nil {
		log.Printf("inspect export supplement clusters: %v", err)
		return aggs
	}
	rows, err := w.Store.ListInspections()
	if err != nil {
		log.Printf("inspect export supplement inspections: %v", err)
		return aggs
	}
	have := make(map[string]struct{}, len(aggs))
	for _, a := range aggs {
		have[a.ClusterID] = struct{}{}
	}
	byID := inspectionMap(rows)
	scope := req.Scope
	if scope == "" {
		scope = ExportScopeAll
	}
	wantID := strings.TrimSpace(req.ClusterID)
	added := false
	for _, c := range clusters {
		if scope == ExportScopeCurrent && c.ID != wantID {
			continue
		}
		if _, ok := have[c.ID]; ok {
			continue
		}
		row, ok := byID[c.ID]
		if !ok {
			if scope == ExportScopeAll {
				aggs = append(aggs, AggCluster{ClusterID: c.ID, ClusterName: c.Name})
				added = true
			}
			continue
		}
		checked := row.CheckedAt
		if checked.Before(from) || checked.After(to) {
			if scope == ExportScopeAll {
				aggs = append(aggs, AggCluster{ClusterID: c.ID, ClusterName: c.Name})
				added = true
			}
			continue
		}
		sec := buildSection(c.Name, &row, from, to, req.KeepAllIssues)
		ac := AggCluster{
			ClusterID:     c.ID,
			ClusterName:   c.Name,
			LastCheckedAt: checked,
			ProbeStatus:   row.ProbeStatus,
			ProbeError:    row.ProbeError,
			Rounds:        1,
			Critical:      sec.crit,
			Error:         sec.errN,
			Warning:       sec.warn,
			Info:          sec.info,
			Summary:       makeSummary(kube.ProbeResult{Status: row.ProbeStatus}, sec.crit, sec.errN, sec.warn, sec.info),
		}
		if sec.loadNote != "" && ac.Summary == "" {
			ac.Summary = sec.loadNote
		}
		for _, it := range sec.issues {
			fp := issueFingerprint(it)
			ai := AggIssue{
				Fingerprint: fp,
				Severity:    it.Severity,
				Category:    it.Category,
				Kind:        it.Kind,
				Namespace:   it.Namespace,
				Name:        it.Name,
				Reason:      it.Reason,
				Message:     clipMsg(it.Message, maxDumpMessageRunes),
				Since:       it.Since,
				SinceAt:     it.SinceAt,
				FirstSeen:   checked,
				LastSeen:    checked,
				SeenRounds:  1,
				SeenDays:    1,
			}
			if loc != nil {
				ai.DayKeys = []string{checked.In(loc).Format("2006-01-02")}
			}
			ac.Issues = append(ac.Issues, ai)
		}
		aggs = append(aggs, ac)
		added = true
	}
	if added {
		sort.SliceStable(aggs, func(i, j int) bool {
			return aggs[i].ClusterName < aggs[j].ClusterName
		})
	}
	return aggs
}
