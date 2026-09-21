package inspect

import (
	"fmt"
	"strings"
	"time"

	"gitee.com/k8man/internal/kube"
)

// buildMarkdownFromAgg turns aggregated file data into the export report markdown.
func buildMarkdownFromAgg(req ExportRequest, clusters []AggCluster, from, to time.Time, loc *time.Location, sourceNote string) *ExportReport {
	if loc == nil {
		loc = beijingLoc
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	period := req.Period
	if period == "" {
		period = ExportPeriodWeek
	}
	scope := req.Scope
	if scope == "" {
		scope = ExportScopeAll
	}

	wantID := strings.TrimSpace(req.ClusterID)
	filtered := make([]AggCluster, 0, len(clusters))
	for _, c := range clusters {
		if scope == ExportScopeCurrent && c.ClusterID != wantID {
			continue
		}
		filtered = append(filtered, c)
	}

	scopeText := scopeLabel(scope)
	if scope == ExportScopeCurrent && len(filtered) == 1 {
		name := filtered[0].ClusterName
		if name == "" {
			name = filtered[0].ClusterID
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
	filename := fmt.Sprintf("k8man-inspect-%s-%s-%s.md", string(period), string(scope), fileStamp)

	var totalCrit, totalErr, totalWarn, totalInfo, checkedN int
	ids := make([]string, 0, len(filtered))
	for _, c := range filtered {
		ids = append(ids, c.ClusterID)
		totalCrit += c.Critical
		totalErr += c.Error
		totalWarn += c.Warning
		totalInfo += c.Info
		if !c.LastCheckedAt.IsZero() {
			checkedN++
		}
	}

	showDuration := period == ExportPeriodWeek || period == ExportPeriodMonth
	filterNote := "数据来自 PVC inspection 目录聚合（问题指纹去重"
	if showDuration {
		filterNote += "，含持续天数"
	}
	filterNote += "）"
	if note := strings.TrimSpace(sourceNote); note != "" {
		filterNote += "；" + note
	}

	summary := fmt.Sprintf(
		"周期=%s；范围=%s；窗口 %s ~ %s；基地 %d（有数据 %d）；问题 严重%d / 错误%d / 警告%d / 提示%d（%s）",
		periodText, scopeText,
		fmtTimeLocal(from, loc), fmtTimeLocal(to, loc),
		len(filtered), checkedN,
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
	b.WriteString("- 数据来源：")
	b.WriteString(escapeMD(filterNote))
	b.WriteString("\n")
	if note := strings.TrimSpace(req.ExtraNote); note != "" {
		b.WriteString("- ")
		b.WriteString(escapeMD(note))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## 总览\n\n")
	if showDuration {
		b.WriteString("| 基地 | 最近巡检 | Probe | 严重 | 错误 | 警告 | 提示 | 摘要 |\n")
	} else {
		b.WriteString("| 基地 | 最近巡检 | 轮次 | Probe | 严重 | 错误 | 警告 | 提示 | 摘要 |\n")
	}
	b.WriteString("| --- | --- |")
	if !showDuration {
		b.WriteString(" ---: |")
	}
	b.WriteString(" --- | ---: | ---: | ---: | ---: | --- |\n")
	for _, c := range filtered {
		name := c.ClusterName
		if name == "" {
			name = c.ClusterID
		}
		checked := "—"
		if !c.LastCheckedAt.IsZero() {
			checked = fmtTimeLocal(c.LastCheckedAt, loc)
		}
		probe := c.ProbeStatus
		if probe == "" {
			probe = "—"
		}
		sum := c.Summary
		if sum == "" {
			sum = makeSummary(kube.ProbeResult{Status: c.ProbeStatus}, c.Critical, c.Error, c.Warning, c.Info)
		}
		if showDuration {
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %d | %s |\n",
				escapeMD(name), checked, escapeMD(probe),
				c.Critical, c.Error, c.Warning, c.Info, escapeMD(sum))
		} else {
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %d | %d | %d | %d | %s |\n",
				escapeMD(name), checked, c.Rounds, escapeMD(probe),
				c.Critical, c.Error, c.Warning, c.Info, escapeMD(sum))
		}
	}
	b.WriteString("\n")

	for _, c := range filtered {
		name := c.ClusterName
		if name == "" {
			name = c.ClusterID
		}
		b.WriteString("## 基地：")
		b.WriteString(escapeMD(name))
		b.WriteString("\n\n")
		if c.LastCheckedAt.IsZero() && len(c.Issues) == 0 {
			b.WriteString("无巡检文件数据。\n\n")
			continue
		}
		b.WriteString("- 最近巡检：")
		b.WriteString(fmtTimeLocal(c.LastCheckedAt, loc))
		b.WriteString("\n")
		if c.Rounds > 0 && !showDuration {
			fmt.Fprintf(&b, "- 当日轮次：%d\n", c.Rounds)
		}
		b.WriteString("- Probe：")
		b.WriteString(escapeMD(c.ProbeStatus))
		if c.ProbeError != "" {
			b.WriteString("（")
			b.WriteString(escapeMD(c.ProbeError))
			b.WriteString("）")
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "- 问题合计：严重 %d / 错误 %d / 警告 %d / 提示 %d\n\n", c.Critical, c.Error, c.Warning, c.Info)

		issues := c.Issues
		if len(issues) > maxIssuesPerCluster {
			b.WriteString(fmt.Sprintf("（问题过多，仅列出前 %d 条）\n\n", maxIssuesPerCluster))
			issues = issues[:maxIssuesPerCluster]
		}
		if len(issues) == 0 {
			b.WriteString("无问题。\n\n")
			continue
		}
		b.WriteString("### 问题列表\n\n")
		for _, it := range issues {
			b.WriteString(aggIssueLine(it, showDuration))
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
	}
}

func aggIssueLine(it AggIssue, showDuration bool) string {
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
	if showDuration && it.SeenDays > 0 {
		line += fmt.Sprintf("（持续 %d 天", it.SeenDays)
		if !it.FirstSeen.IsZero() && !it.LastSeen.IsZero() {
			line += "，" + it.FirstSeen.In(beijingLoc).Format("01-02") + "~" + it.LastSeen.In(beijingLoc).Format("01-02")
		}
		line += "）"
	} else if it.SeenRounds > 1 {
		line += fmt.Sprintf("（当日出现 %d 轮）", it.SeenRounds)
	} else if it.Since != "" {
		line += "（" + escapeMD(it.Since) + "）"
	}
	return line
}
