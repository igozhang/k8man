package inspect

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gitee.com/k8man/internal/model"
	"gitee.com/k8man/internal/store"
)

const (
	// DailyArchiveMinClusters is exclusive: archive only when kubeconfig count is greater than 2.
	DailyArchiveMinClusters = 2
	dailyArchiveHour        = 1
	dailyArchiveRetry       = 10 * time.Minute
	dailyArchiveNote        = "系统自动归档：次日约 01:00（Asia/Shanghai）写入前一自然日、所有集群的巡检报告；数据来自 PVC inspection 目录当日轮次 dump 的问题并集，归档后清理该日轮次目录"
)

func dailyArchiveSlug(day time.Time, loc *time.Location) string {
	return AllDaySlug(day, loc)
}

// AllDaySlug is the public doc id/slug for one calendar day's all-cluster report.
func AllDaySlug(day time.Time, loc *time.Location) string {
	if loc == nil {
		loc = beijingLoc
	}
	return "inspectall" + day.In(loc).Format("20060102")
}

var (
	reAllDaySlug = regexp.MustCompile(`^inspectall(\d{8})$`)
	reOldDaySlug = regexp.MustCompile(`^inspect-daily-all-(\d{8})$`)
	reTitleDate  = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})`)
)

// previousCalendarDay is [yesterday 00:00, today 00:00) expressed as inclusive end = today 00:00 - 1ns.
func previousCalendarDay(now time.Time, loc *time.Location) (from, to, day time.Time) {
	if loc == nil {
		loc = beijingLoc
	}
	now = now.In(loc)
	y, m, d := now.Date()
	startToday := time.Date(y, m, d, 0, 0, 0, 0, loc)
	from = startToday.AddDate(0, 0, -1)
	to = startToday.Add(-time.Nanosecond)
	day = from
	return from, to, day
}

func dailyArchiveOpen(now time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = beijingLoc
	}
	now = now.In(loc)
	y, m, d := now.Date()
	gate := time.Date(y, m, d, dailyArchiveHour, 0, 0, 0, loc)
	return !now.Before(gate)
}

// nextDailyArchiveAt is the next 01:00 plus jitter (jitter is clamped to [0, 15m]).
func nextDailyArchiveAt(now time.Time, loc *time.Location, jitter time.Duration) time.Time {
	if loc == nil {
		loc = beijingLoc
	}
	if jitter < 0 {
		jitter = -jitter
	}
	if jitter > 15*time.Minute {
		jitter = 15 * time.Minute
	}
	now = now.In(loc)
	y, m, d := now.Date()
	next := time.Date(y, m, d, dailyArchiveHour, 0, 0, 0, loc).Add(jitter)
	if !now.Before(next) {
		next = time.Date(y, m, d, dailyArchiveHour, 0, 0, 0, loc).AddDate(0, 0, 1).Add(jitter)
	}
	return next
}

func (w *Worker) dailyArchiveLoop(ctx context.Context) {
	if w == nil || w.Store == nil {
		return
	}
	if err := w.RepairAllDayDocIDs(); err != nil {
		log.Printf("inspect daily archive rekey: %v", err)
	}
	for {
		if ctx.Err() != nil {
			return
		}
		err := w.archiveOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("inspect daily archive: %v", err)
			if sleepCtx(ctx, dailyArchiveRetry) != nil {
				return
			}
			continue
		}
		nowFn := w.now
		if nowFn == nil {
			nowFn = time.Now
		}
		wait := time.Until(nextDailyArchiveAt(nowFn(), beijingLoc, w.archiveJitter()))
		if wait < time.Second {
			wait = time.Second
		}
		if sleepCtx(ctx, wait) != nil {
			return
		}
	}
}

func (w *Worker) archiveOnce(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	nowFn := w.now
	if nowFn == nil {
		nowFn = time.Now
	}
	return w.archivePreviousDayIfDue(ctx, nowFn())
}

func (w *Worker) archiveJitter() time.Duration {
	if w == nil || w.jitter == nil {
		return 0
	}
	d := w.jitter()
	if d < 0 {
		d = -d
	}
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}

// archivePreviousDayIfDue writes yesterday's all-cluster report when it is past 01:00
// and more than two kubeconfigs are saved. Same calendar day is idempotent via slug.
func (w *Worker) archivePreviousDayIfDue(ctx context.Context, now time.Time) error {
	if w == nil || w.Store == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err := w.RepairAllDayDocIDs(); err != nil {
		return err
	}
	w.dailyMu.Lock()
	defer w.dailyMu.Unlock()
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	loc := beijingLoc
	if !dailyArchiveOpen(now, loc) {
		return nil
	}
	n, err := w.kubeconfigCount()
	if err != nil {
		return err
	}
	if n <= DailyArchiveMinClusters {
		return nil
	}
	_, _, day := previousCalendarDay(now, loc)
	slug := dailyArchiveSlug(day, loc)
	existing, err := w.Store.GetDoc(slug)
	if err != nil {
		return err
	}
	if existing == nil {
		existing, err = w.Store.GetDocBySlug(slug)
		if err != nil {
			return err
		}
	}
	if existing != nil {
		return nil
	}
	from, to, _ := previousCalendarDay(now, loc)
	if w.Files != nil {
		rounds, err := w.Files.loadAllRounds(day, loc)
		if err != nil {
			return err
		}
		loaded, err := w.Files.LoadDay(day, loc)
		if err != nil {
			return err
		}
		if len(rounds) == 0 && len(loaded) == 0 {
			if err := w.Files.SeedRoundFromSQLiteForDay(w.Store, day, loc); err != nil {
				log.Printf("inspect daily archive seed: %v", err)
			}
		}
		if _, err := w.Files.EnsureDayAggregate(day, loc); err != nil {
			return err
		}
		if err := w.Files.PruneOlderThan(w.retentionDays, now, loc); err != nil {
			log.Printf("inspect prune: %v", err)
		}
	}
	rep, err := w.BuildExport(ExportRequest{
		Period:        ExportPeriodDay,
		Scope:         ExportScopeAll,
		Now:           now,
		Location:      loc,
		WindowFrom:    from,
		WindowTo:      to,
		TitleAt:       day,
		ExtraNote:     dailyArchiveNote,
		KeepAllIssues: true,
	})
	if err != nil {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	// Re-check so a concurrent writer or a delete-during-build race still hits the unique slug.
	existing, err = w.Store.GetDocBySlug(slug)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	created := now.UTC()
	summary := strings.TrimSpace(rep.Summary)
	if utf8.RuneCountInString(summary) > 500 {
		summary = clipRunesMax(summary, 500)
	}
	title := strings.TrimSpace(rep.Title)
	if title == "" {
		title = "巡检报告 · 日 · 所有基地 · " + day.In(loc).Format("2006-01-02")
	}
	d := &model.Doc{
		ID:        slug,
		Title:     title,
		Slug:      slug,
		Category:  DocCategoryInspectionReport,
		Tags:      []string{"巡检报告", "day", "all", "自动"},
		Summary:   summary,
		Content:   rep.Markdown,
		CreatedAt: created,
		UpdatedAt: created,
	}
	if strings.TrimSpace(d.Content) == "" {
		return fmt.Errorf("empty daily archive markdown")
	}
	if err := w.Store.CreateDoc(d); err != nil {
		if store.IsUniqueErr(err) {
			return nil
		}
		return err
	}
	log.Printf("inspect daily archive saved doc=%s slug=%s clusters=%d", d.ID, slug, n)
	return nil
}

func (w *Worker) kubeconfigCount() (int, error) {
	list, err := w.Store.ListClusters()
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

func allDaySlugForDoc(d model.Doc) (string, bool) {
	if m := reAllDaySlug.FindStringSubmatch(strings.TrimSpace(d.Slug)); m != nil {
		return "inspectall" + m[1], true
	}
	if m := reOldDaySlug.FindStringSubmatch(strings.TrimSpace(d.Slug)); m != nil {
		return "inspectall" + m[1], true
	}
	title := strings.TrimSpace(d.Title)
	if strings.Contains(title, "· 日 ·") && strings.Contains(title, "所有基地") {
		if m := reTitleDate.FindStringSubmatch(title); m != nil {
			return "inspectall" + m[1] + m[2] + m[3], true
		}
	}
	if m := reAllDaySlug.FindStringSubmatch(strings.TrimSpace(d.ID)); m != nil {
		return d.ID, true
	}
	return "", false
}

// RepairAllDayDocIDs rewrites older UUID docs so /docs/inspectallYYYYMMDD is the address.
func (w *Worker) RepairAllDayDocIDs() error {
	if w == nil || w.Store == nil {
		return nil
	}
	docs, err := w.Store.ListDocs(DocCategoryInspectionReport, "")
	if err != nil {
		return err
	}
	chosen := map[string]model.Doc{}
	for _, d := range docs {
		slug, ok := allDaySlugForDoc(d)
		if !ok || slug == "" {
			continue
		}
		prev, exists := chosen[slug]
		if !exists || d.ID == slug || (prev.ID != slug && d.UpdatedAt.After(prev.UpdatedAt)) {
			chosen[slug] = d
		}
	}
	for slug, d := range chosen {
		if d.ID == slug && d.Slug == slug {
			continue
		}
		if d.ID != slug {
			cur, err := w.Store.GetDoc(slug)
			if err != nil {
				return err
			}
			if cur != nil {
				continue
			}
		}
		if err := w.Store.RekeyDoc(d.ID, slug); err != nil {
			if store.IsUniqueErr(err) {
				log.Printf("inspect daily archive rekey skip %s -> %s: %v", d.ID, slug, err)
				continue
			}
			return err
		}
		log.Printf("inspect daily archive rekey %s -> %s", d.ID, slug)
	}
	return nil
}
