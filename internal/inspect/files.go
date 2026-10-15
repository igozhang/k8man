package inspect

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/store"
)

const (
	InspectionDirName    = "inspection"
	DefaultRetentionDays = 365
	DefaultRetentionMax  = 365
	dayFileName          = "day.jsonl"
	dayIndexName         = "day_index.txt"
	roundIndexName       = "_index.txt"
	maxDumpMessageRunes  = 400
	maxDumpEvidence      = 5
)

// FileStore writes/reads PVC inspection dumps under <dataDir>/inspection.
type FileStore struct {
	Root string // absolute or relative path to inspection/
	mu   sync.Mutex
}

func NewFileStore(dataDir string) *FileStore {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = "./data"
	}
	return &FileStore{Root: filepath.Join(dataDir, InspectionDirName)}
}

func (fs *FileStore) dayDir(day time.Time, loc *time.Location) string {
	if loc == nil {
		loc = beijingLoc
	}
	d := day.In(loc)
	return filepath.Join(fs.Root, d.Format("2006"), d.Format("01"), d.Format("02"))
}

func (fs *FileStore) dayFile(day time.Time, loc *time.Location) string {
	return filepath.Join(fs.dayDir(day, loc), dayFileName)
}

func (fs *FileStore) roundDir(checkedAt time.Time, loc *time.Location) string {
	if loc == nil {
		loc = beijingLoc
	}
	t := checkedAt.In(loc)
	base := filepath.Join(fs.dayDir(t, loc), t.Format("150405"))
	return base
}

func issueFingerprint(it kube.Issue) string {
	return strings.Join([]string{
		strings.TrimSpace(it.Category),
		strings.TrimSpace(it.Kind),
		strings.TrimSpace(it.Namespace),
		strings.TrimSpace(it.Name),
		strings.TrimSpace(it.Reason),
	}, "|")
}

func clipMsg(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

var reRoundDirName = regexp.MustCompile(`^\d{6}$`)

// DumpRound writes one cluster's inspection round as readable JSONL under the day/HHMMSS dir.
func (fs *FileStore) DumpRound(clusterName string, row *store.InspectionRow, issues []kube.Issue, loc *time.Location) error {
	if fs == nil || row == nil || strings.TrimSpace(row.ClusterID) == "" {
		return nil
	}
	if loc == nil {
		loc = beijingLoc
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir := fs.roundDir(row.CheckedAt, loc)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(dir, sanitizeFilePart(row.ClusterID)+".jsonl")
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)

	meta := map[string]any{
		"type":        "meta",
		"clusterId":   row.ClusterID,
		"clusterName": strings.TrimSpace(clusterName),
		"checkedAt":   row.CheckedAt.UTC().Format(time.RFC3339),
		"probeStatus": row.ProbeStatus,
		"probeError":  clipMsg(row.ProbeError, 200),
		"critical":    row.Critical,
		"error":       row.Error,
		"warning":     row.Warning,
		"info":        row.Info,
		"summary":     row.Summary,
		"elapsedMs":   row.ElapsedMs,
	}
	if err := writeJSONLine(w, meta); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	for _, it := range issues {
		line := map[string]any{
			"type":      "issue",
			"fp":        issueFingerprint(it),
			"severity":  it.Severity,
			"category":  it.Category,
			"kind":      it.Kind,
			"namespace": it.Namespace,
			"name":      it.Name,
			"reason":    it.Reason,
			"message":   clipMsg(it.Message, maxDumpMessageRunes),
			"since":     it.Since,
			"sinceAt":   it.SinceAt,
			"node":      it.Node,
			"nodeIP":    it.NodeIP,
			"container": it.Container,
		}
		if len(it.Evidence) > 0 {
			ev := it.Evidence
			if len(ev) > maxDumpEvidence {
				ev = ev[:maxDumpEvidence]
			}
			line["evidence"] = ev
		}
		if err := writeJSONLine(w, line); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return fs.appendRoundIndex(dir, row, clusterName)
}

func (fs *FileStore) appendRoundIndex(dir string, row *store.InspectionRow, clusterName string) error {
	path := filepath.Join(dir, roundIndexName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	name := strings.TrimSpace(clusterName)
	if name == "" {
		name = row.ClusterID
	}
	line := fmt.Sprintf("%s\t%s\t%s\tcrit=%d err=%d warn=%d info=%d\t%s\n",
		row.ClusterID,
		name,
		row.CheckedAt.UTC().Format(time.RFC3339),
		row.Critical, row.Error, row.Warning, row.Info,
		strings.ReplaceAll(row.Summary, "\t", " "),
	)
	_, err = f.WriteString(line)
	return err
}

func writeJSONLine(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return w.WriteByte('\n')
}

func sanitizeFilePart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

// RoundCluster is one cluster snapshot loaded from a round JSONL.
type RoundCluster struct {
	ClusterID   string
	ClusterName string
	CheckedAt   time.Time
	ProbeStatus string
	ProbeError  string
	Critical    int
	Error       int
	Warning     int
	Info        int
	Summary     string
	Issues      []kube.Issue
}

func loadRoundJSONL(path string) (*RoundCluster, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := &RoundCluster{Issues: []kube.Issue{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		var typ string
		_ = json.Unmarshal(raw["type"], &typ)
		switch typ {
		case "meta":
			var m struct {
				ClusterID   string `json:"clusterId"`
				ClusterName string `json:"clusterName"`
				CheckedAt   string `json:"checkedAt"`
				ProbeStatus string `json:"probeStatus"`
				ProbeError  string `json:"probeError"`
				Critical    int    `json:"critical"`
				Error       int    `json:"error"`
				Warning     int    `json:"warning"`
				Info        int    `json:"info"`
				Summary     string `json:"summary"`
			}
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			out.ClusterID = m.ClusterID
			out.ClusterName = m.ClusterName
			out.ProbeStatus = m.ProbeStatus
			out.ProbeError = m.ProbeError
			out.Critical = m.Critical
			out.Error = m.Error
			out.Warning = m.Warning
			out.Info = m.Info
			out.Summary = m.Summary
			if t, err := time.Parse(time.RFC3339, m.CheckedAt); err == nil {
				out.CheckedAt = t
			}
		case "issue":
			var it struct {
				FP        string   `json:"fp"`
				Severity  string   `json:"severity"`
				Category  string   `json:"category"`
				Kind      string   `json:"kind"`
				Namespace string   `json:"namespace"`
				Name      string   `json:"name"`
				Reason    string   `json:"reason"`
				Message   string   `json:"message"`
				Since     string   `json:"since"`
				SinceAt   string   `json:"sinceAt"`
				Node      string   `json:"node"`
				NodeIP    string   `json:"nodeIP"`
				Container string   `json:"container"`
				Evidence  []string `json:"evidence"`
			}
			if err := json.Unmarshal([]byte(line), &it); err != nil {
				continue
			}
			issue := kube.Issue{
				Severity:  it.Severity,
				Category:  it.Category,
				Kind:      it.Kind,
				Namespace: it.Namespace,
				Name:      it.Name,
				Reason:    it.Reason,
				Message:   it.Message,
				Since:     it.Since,
				SinceAt:   it.SinceAt,
				Node:      it.Node,
				NodeIP:    it.NodeIP,
				Container: it.Container,
				Evidence:  it.Evidence,
			}
			out.Issues = append(out.Issues, issue)
		}
	}
	if out.ClusterID == "" {
		return nil, fmt.Errorf("no meta in %s", path)
	}
	return out, sc.Err()
}

func (fs *FileStore) listRoundDirs(day time.Time, loc *time.Location) ([]string, error) {
	dir := fs.dayDir(day, loc)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if reRoundDirName.MatchString(name) {
			out = append(out, filepath.Join(dir, name))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (fs *FileStore) loadAllRounds(day time.Time, loc *time.Location) ([]*RoundCluster, error) {
	dirs, err := fs.listRoundDirs(day, loc)
	if err != nil {
		return nil, err
	}
	var out []*RoundCluster
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			rc, err := loadRoundJSONL(filepath.Join(d, e.Name()))
			if err != nil {
				continue
			}
			out = append(out, rc)
		}
	}
	return out, nil
}

// AggIssue is a deduped issue with presence across rounds/days.
type AggIssue struct {
	Fingerprint string    `json:"fp"`
	Severity    string    `json:"severity"`
	Category    string    `json:"category"`
	Kind        string    `json:"kind"`
	Namespace   string    `json:"namespace,omitempty"`
	Name        string    `json:"name"`
	Reason      string    `json:"reason"`
	Message     string    `json:"message,omitempty"`
	Since       string    `json:"since,omitempty"`
	SinceAt     string    `json:"sinceAt,omitempty"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	SeenRounds  int       `json:"seenRounds,omitempty"`
	SeenDays    int       `json:"seenDays,omitempty"`
	DayKeys     []string  `json:"dayKeys,omitempty"` // YYYY-MM-DD
}

// AggCluster is per-cluster aggregation for a day or range.
type AggCluster struct {
	ClusterID     string     `json:"clusterId"`
	ClusterName   string     `json:"clusterName"`
	LastCheckedAt time.Time  `json:"lastCheckedAt"`
	ProbeStatus   string     `json:"probeStatus,omitempty"`
	ProbeError    string     `json:"probeError,omitempty"`
	Rounds        int        `json:"rounds,omitempty"`
	Critical      int        `json:"critical"`
	Error         int        `json:"error"`
	Warning       int        `json:"warning"`
	Info          int        `json:"info"`
	Summary       string     `json:"summary,omitempty"`
	Issues        []AggIssue `json:"-"`
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "error":
		return 1
	case "warning":
		return 2
	case "info":
		return 3
	default:
		return 2
	}
}

func mergeIssue(dst *AggIssue, it kube.Issue, seenAt time.Time) {
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	if dst.FirstSeen.IsZero() || seenAt.Before(dst.FirstSeen) {
		dst.FirstSeen = seenAt
	}
	if dst.LastSeen.IsZero() || seenAt.After(dst.LastSeen) {
		dst.LastSeen = seenAt
		dst.Message = clipMsg(it.Message, maxDumpMessageRunes)
		dst.Since = it.Since
		dst.SinceAt = it.SinceAt
	}
	if severityRank(it.Severity) < severityRank(dst.Severity) {
		dst.Severity = it.Severity
	}
	dst.SeenRounds++
	if dst.Category == "" {
		dst.Category = it.Category
		dst.Kind = it.Kind
		dst.Namespace = it.Namespace
		dst.Name = it.Name
		dst.Reason = it.Reason
	}
}

// AggregateDay unions round dumps into day.jsonl (merging any existing day.jsonl), then removes round dirs.
// With no leftover rounds it returns the existing day.jsonl (or nil).
func (fs *FileStore) AggregateDay(day time.Time, loc *time.Location) ([]AggCluster, error) {
	if fs == nil {
		return nil, fmt.Errorf("file store nil")
	}
	if loc == nil {
		loc = beijingLoc
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	existing, err := fs.loadDayUnlocked(day, loc)
	if err != nil {
		return nil, err
	}
	rounds, err := fs.loadAllRounds(day, loc)
	if err != nil {
		return nil, err
	}
	if len(rounds) == 0 {
		return existing, nil
	}
	fromRounds := aggregateRounds(rounds, day, loc)
	out := mergeDayAggs(existing, fromRounds, day, loc)
	if err := fs.writeDayFile(day, loc, out); err != nil {
		return out, err
	}
	if err := fs.removeRoundDirs(day, loc); err != nil {
		return out, err
	}
	return out, nil
}

// PeekDay unions live round dumps with day.jsonl when both exist (does not finalize).
func (fs *FileStore) PeekDay(day time.Time, loc *time.Location) ([]AggCluster, error) {
	if fs == nil {
		return nil, nil
	}
	if loc == nil {
		loc = beijingLoc
	}
	existing, err := fs.LoadDay(day, loc)
	if err != nil {
		return nil, err
	}
	rounds, err := fs.loadAllRounds(day, loc)
	if err != nil {
		return nil, err
	}
	if len(rounds) == 0 {
		return existing, nil
	}
	fromRounds := aggregateRounds(rounds, day, loc)
	if len(existing) == 0 {
		return fromRounds, nil
	}
	return mergeDayAggs(existing, fromRounds, day, loc), nil
}

// mergeDayAggs unions two same-day aggregations (e.g. prior day.jsonl + leftover rounds).
func mergeDayAggs(base, extra []AggCluster, day time.Time, loc *time.Location) []AggCluster {
	if len(base) == 0 {
		return extra
	}
	if len(extra) == 0 {
		return base
	}
	if loc == nil {
		loc = beijingLoc
	}
	byID := map[string]*AggCluster{}
	issueMap := map[string]map[string]*AggIssue{}

	ingest := func(list []AggCluster) {
		for _, dc := range list {
			ac, ok := byID[dc.ClusterID]
			if !ok {
				cp := dc
				cp.Issues = nil
				ac = &cp
				byID[dc.ClusterID] = ac
				issueMap[dc.ClusterID] = map[string]*AggIssue{}
			} else {
				ac.Rounds += dc.Rounds
				if dc.LastCheckedAt.After(ac.LastCheckedAt) {
					ac.LastCheckedAt = dc.LastCheckedAt
					ac.ProbeStatus = dc.ProbeStatus
					ac.ProbeError = dc.ProbeError
					if dc.ClusterName != "" {
						ac.ClusterName = dc.ClusterName
					}
				}
			}
			im := issueMap[dc.ClusterID]
			for _, it := range dc.Issues {
				fp := it.Fingerprint
				if fp == "" {
					fp = issueFingerprint(kube.Issue{
						Category: it.Category, Kind: it.Kind, Namespace: it.Namespace,
						Name: it.Name, Reason: it.Reason,
					})
				}
				if fp == "" || fp == "||||" {
					continue
				}
				ai, ok := im[fp]
				if !ok {
					cp := it
					cp.Fingerprint = fp
					im[fp] = &cp
					continue
				}
				if it.FirstSeen.Before(ai.FirstSeen) || ai.FirstSeen.IsZero() {
					ai.FirstSeen = it.FirstSeen
				}
				if it.LastSeen.After(ai.LastSeen) || ai.LastSeen.IsZero() {
					ai.LastSeen = it.LastSeen
					ai.Message = it.Message
					ai.Since = it.Since
					ai.SinceAt = it.SinceAt
				}
				if severityRank(it.Severity) < severityRank(ai.Severity) {
					ai.Severity = it.Severity
				}
				ai.SeenRounds += it.SeenRounds
			}
		}
	}
	ingest(base)
	ingest(extra)

	out := make([]AggCluster, 0, len(byID))
	dayKey := day.In(loc).Format("2006-01-02")
	for id, ac := range byID {
		issues := make([]AggIssue, 0, len(issueMap[id]))
		for _, ai := range issueMap[id] {
			ai.SeenDays = 1
			ai.DayKeys = []string{dayKey}
			issues = append(issues, *ai)
		}
		sort.SliceStable(issues, func(i, j int) bool {
			ri, rj := severityRank(issues[i].Severity), severityRank(issues[j].Severity)
			if ri != rj {
				return ri < rj
			}
			return issues[i].Fingerprint < issues[j].Fingerprint
		})
		ac.Issues = issues
		ac.Critical, ac.Error, ac.Warning, ac.Info = countAggIssues(issues)
		ac.Summary = makeSummary(kube.ProbeResult{Status: ac.ProbeStatus}, ac.Critical, ac.Error, ac.Warning, ac.Info)
		out = append(out, *ac)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ClusterName < out[j].ClusterName
	})
	return out
}

func aggregateRounds(rounds []*RoundCluster, day time.Time, loc *time.Location) []AggCluster {
	if loc == nil {
		loc = beijingLoc
	}
	byID := map[string]*AggCluster{}
	issueMap := map[string]map[string]*AggIssue{}

	for _, rc := range rounds {
		ac, ok := byID[rc.ClusterID]
		if !ok {
			ac = &AggCluster{
				ClusterID:   rc.ClusterID,
				ClusterName: rc.ClusterName,
			}
			byID[rc.ClusterID] = ac
			issueMap[rc.ClusterID] = map[string]*AggIssue{}
		}
		ac.Rounds++
		if rc.CheckedAt.After(ac.LastCheckedAt) {
			ac.LastCheckedAt = rc.CheckedAt
			ac.ProbeStatus = rc.ProbeStatus
			ac.ProbeError = rc.ProbeError
			ac.ClusterName = rc.ClusterName
		}
		seenAt := rc.CheckedAt
		if seenAt.IsZero() {
			// Prefer calendar day noon over wall clock so historical dumps stay dated correctly.
			d := day.In(loc)
			seenAt = time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc)
		}
		for _, it := range rc.Issues {
			fp := issueFingerprint(it)
			if fp == "" || fp == "||||" {
				continue
			}
			ai, ok := issueMap[rc.ClusterID][fp]
			if !ok {
				ai = &AggIssue{
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
				}
				issueMap[rc.ClusterID][fp] = ai
			}
			mergeIssue(ai, it, seenAt)
		}
	}

	out := make([]AggCluster, 0, len(byID))
	dayKey := day.In(loc).Format("2006-01-02")
	for id, ac := range byID {
		issues := make([]AggIssue, 0, len(issueMap[id]))
		for _, ai := range issueMap[id] {
			ai.SeenDays = 1
			ai.DayKeys = []string{dayKey}
			issues = append(issues, *ai)
		}
		sort.SliceStable(issues, func(i, j int) bool {
			ri, rj := severityRank(issues[i].Severity), severityRank(issues[j].Severity)
			if ri != rj {
				return ri < rj
			}
			return issues[i].Fingerprint < issues[j].Fingerprint
		})
		ac.Issues = issues
		ac.Critical, ac.Error, ac.Warning, ac.Info = countAggIssues(issues)
		ac.Summary = makeSummary(kube.ProbeResult{Status: ac.ProbeStatus}, ac.Critical, ac.Error, ac.Warning, ac.Info)
		out = append(out, *ac)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ClusterName < out[j].ClusterName
	})
	return out
}

func countAggIssues(issues []AggIssue) (critical, errorN, warning, info int) {
	for _, it := range issues {
		switch it.Severity {
		case "critical":
			critical++
		case "error":
			errorN++
		case "info":
			info++
		default:
			warning++
		}
	}
	return
}

func (fs *FileStore) writeDayFile(day time.Time, loc *time.Location, clusters []AggCluster) error {
	dir := fs.dayDir(day, loc)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	path := fs.dayFile(day, loc)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	dateKey := day.In(loc).Format("2006-01-02")
	_ = writeJSONLine(w, map[string]any{
		"type":        "day",
		"date":        dateKey,
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"clusters":    len(clusters),
	})
	idx := &strings.Builder{}
	fmt.Fprintf(idx, "# day %s clusters=%d\n", dateKey, len(clusters))
	for _, c := range clusters {
		_ = writeJSONLine(w, map[string]any{
			"type":          "cluster",
			"clusterId":     c.ClusterID,
			"clusterName":   c.ClusterName,
			"lastCheckedAt": c.LastCheckedAt.UTC().Format(time.RFC3339),
			"probeStatus":   c.ProbeStatus,
			"probeError":    clipMsg(c.ProbeError, 200),
			"rounds":        c.Rounds,
			"critical":      c.Critical,
			"error":         c.Error,
			"warning":       c.Warning,
			"info":          c.Info,
			"summary":       c.Summary,
		})
		fmt.Fprintf(idx, "%s\t%s\trounds=%d\tcrit=%d err=%d warn=%d info=%d\t%s\n",
			c.ClusterID, c.ClusterName, c.Rounds, c.Critical, c.Error, c.Warning, c.Info, c.Summary)
		for _, it := range c.Issues {
			_ = writeJSONLine(w, map[string]any{
				"type":       "issue",
				"clusterId":  c.ClusterID,
				"fp":         it.Fingerprint,
				"severity":   it.Severity,
				"category":   it.Category,
				"kind":       it.Kind,
				"namespace":  it.Namespace,
				"name":       it.Name,
				"reason":     it.Reason,
				"message":    it.Message,
				"since":      it.Since,
				"sinceAt":    it.SinceAt,
				"firstSeen":  it.FirstSeen.UTC().Format(time.RFC3339),
				"lastSeen":   it.LastSeen.UTC().Format(time.RFC3339),
				"seenRounds": it.SeenRounds,
				"seenDays":   1,
			})
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, dayIndexName), []byte(idx.String()), 0o640); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (fs *FileStore) removeRoundDirs(day time.Time, loc *time.Location) error {
	dirs, err := fs.listRoundDirs(day, loc)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		_ = os.RemoveAll(d)
	}
	return nil
}

// LoadDay loads day.jsonl; if missing, returns nil, nil.
func (fs *FileStore) LoadDay(day time.Time, loc *time.Location) ([]AggCluster, error) {
	return fs.loadDayUnlocked(day, loc)
}

func (fs *FileStore) loadDayUnlocked(day time.Time, loc *time.Location) ([]AggCluster, error) {
	path := fs.dayFile(day, loc)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	byID := map[string]*AggCluster{}
	order := []string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var typ struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &typ); err != nil {
			continue
		}
		switch typ.Type {
		case "cluster":
			var c struct {
				ClusterID     string `json:"clusterId"`
				ClusterName   string `json:"clusterName"`
				LastCheckedAt string `json:"lastCheckedAt"`
				ProbeStatus   string `json:"probeStatus"`
				ProbeError    string `json:"probeError"`
				Rounds        int    `json:"rounds"`
				Critical      int    `json:"critical"`
				Error         int    `json:"error"`
				Warning       int    `json:"warning"`
				Info          int    `json:"info"`
				Summary       string `json:"summary"`
			}
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				continue
			}
			ac := &AggCluster{
				ClusterID:   c.ClusterID,
				ClusterName: c.ClusterName,
				ProbeStatus: c.ProbeStatus,
				ProbeError:  c.ProbeError,
				Rounds:      c.Rounds,
				Critical:    c.Critical,
				Error:       c.Error,
				Warning:     c.Warning,
				Info:        c.Info,
				Summary:     c.Summary,
				Issues:      []AggIssue{},
			}
			if t, err := time.Parse(time.RFC3339, c.LastCheckedAt); err == nil {
				ac.LastCheckedAt = t
			}
			byID[c.ClusterID] = ac
			order = append(order, c.ClusterID)
		case "issue":
			var raw struct {
				ClusterID  string `json:"clusterId"`
				FP         string `json:"fp"`
				Severity   string `json:"severity"`
				Category   string `json:"category"`
				Kind       string `json:"kind"`
				Namespace  string `json:"namespace"`
				Name       string `json:"name"`
				Reason     string `json:"reason"`
				Message    string `json:"message"`
				Since      string `json:"since"`
				SinceAt    string `json:"sinceAt"`
				FirstSeen  string `json:"firstSeen"`
				LastSeen   string `json:"lastSeen"`
				SeenRounds int    `json:"seenRounds"`
				SeenDays   int    `json:"seenDays"`
			}
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				continue
			}
			it := AggIssue{
				Fingerprint: raw.FP,
				Severity:    raw.Severity,
				Category:    raw.Category,
				Kind:        raw.Kind,
				Namespace:   raw.Namespace,
				Name:        raw.Name,
				Reason:      raw.Reason,
				Message:     raw.Message,
				Since:       raw.Since,
				SinceAt:     raw.SinceAt,
				SeenRounds:  raw.SeenRounds,
				SeenDays:    raw.SeenDays,
			}
			if t, err := time.Parse(time.RFC3339, raw.FirstSeen); err == nil {
				it.FirstSeen = t
			}
			if t, err := time.Parse(time.RFC3339, raw.LastSeen); err == nil {
				it.LastSeen = t
			}
			if it.SeenDays == 0 {
				it.SeenDays = 1
			}
			ac := byID[raw.ClusterID]
			if ac == nil {
				continue
			}
			ac.Issues = append(ac.Issues, it)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]AggCluster, 0, len(order))
	for _, id := range order {
		if ac := byID[id]; ac != nil {
			out = append(out, *ac)
		}
	}
	return out, nil
}

// EnsureDayAggregate finalizes leftover rounds into day.jsonl (merging existing), then returns the day view.
func (fs *FileStore) EnsureDayAggregate(day time.Time, loc *time.Location) ([]AggCluster, error) {
	return fs.AggregateDay(day, loc)
}

// AggregateRange merges day files in [fromDay, toDay] inclusive (calendar dates in loc).
// Past days still holding round dumps are finalized into day.jsonl; today is peeked only.
func (fs *FileStore) AggregateRange(fromDay, toDay time.Time, loc *time.Location, now time.Time) ([]AggCluster, error) {
	if loc == nil {
		loc = beijingLoc
	}
	if now.IsZero() {
		now = time.Now()
	}
	fromDay = time.Date(fromDay.In(loc).Year(), fromDay.In(loc).Month(), fromDay.In(loc).Day(), 0, 0, 0, 0, loc)
	toDay = time.Date(toDay.In(loc).Year(), toDay.In(loc).Month(), toDay.In(loc).Day(), 0, 0, 0, 0, loc)
	if toDay.Before(fromDay) {
		return nil, nil
	}
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)

	type key struct{ cluster, fp string }
	clusters := map[string]*AggCluster{}
	issues := map[key]*AggIssue{}
	daySeen := map[key]map[string]struct{}{}

	for d := fromDay; !d.After(toDay); d = d.AddDate(0, 0, 1) {
		dayKey := d.Format("2006-01-02")
		var dayClusters []AggCluster
		var err error
		if d.Before(today) {
			dayClusters, err = fs.EnsureDayAggregate(d, loc)
		} else {
			dayClusters, err = fs.PeekDay(d, loc)
		}
		if err != nil {
			return nil, err
		}
		for _, dc := range dayClusters {
			ac, ok := clusters[dc.ClusterID]
			if !ok {
				cp := dc
				cp.Issues = nil
				ac = &cp
				clusters[dc.ClusterID] = ac
			}
			if dc.LastCheckedAt.After(ac.LastCheckedAt) {
				ac.LastCheckedAt = dc.LastCheckedAt
				ac.ProbeStatus = dc.ProbeStatus
				ac.ProbeError = dc.ProbeError
				ac.ClusterName = dc.ClusterName
			}
			ac.Rounds += dc.Rounds
			for _, it := range dc.Issues {
				k := key{dc.ClusterID, it.Fingerprint}
				ai, ok := issues[k]
				if !ok {
					cp := it
					cp.SeenDays = 0
					cp.DayKeys = nil
					ai = &cp
					issues[k] = ai
					daySeen[k] = map[string]struct{}{}
				}
				if it.FirstSeen.Before(ai.FirstSeen) || ai.FirstSeen.IsZero() {
					ai.FirstSeen = it.FirstSeen
				}
				if it.LastSeen.After(ai.LastSeen) {
					ai.LastSeen = it.LastSeen
					ai.Message = it.Message
					ai.Since = it.Since
					ai.SinceAt = it.SinceAt
				}
				if severityRank(it.Severity) < severityRank(ai.Severity) {
					ai.Severity = it.Severity
				}
				ai.SeenRounds += it.SeenRounds
				if _, exists := daySeen[k][dayKey]; !exists {
					daySeen[k][dayKey] = struct{}{}
					ai.DayKeys = append(ai.DayKeys, dayKey)
				}
			}
		}
	}

	out := make([]AggCluster, 0, len(clusters))
	for id, ac := range clusters {
		list := []AggIssue{}
		for k, ai := range issues {
			if k.cluster != id {
				continue
			}
			ai.SeenDays = len(daySeen[k])
			if ai.SeenDays == 0 {
				ai.SeenDays = 1
			}
			sort.Strings(ai.DayKeys)
			list = append(list, *ai)
		}
		sort.SliceStable(list, func(i, j int) bool {
			ri, rj := severityRank(list[i].Severity), severityRank(list[j].Severity)
			if ri != rj {
				return ri < rj
			}
			if list[i].SeenDays != list[j].SeenDays {
				return list[i].SeenDays > list[j].SeenDays
			}
			return list[i].Fingerprint < list[j].Fingerprint
		})
		ac.Issues = list
		ac.Critical, ac.Error, ac.Warning, ac.Info = countAggIssues(list)
		ac.Summary = makeSummary(kube.ProbeResult{Status: ac.ProbeStatus}, ac.Critical, ac.Error, ac.Warning, ac.Info)
		out = append(out, *ac)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ClusterName < out[j].ClusterName
	})
	return out, nil
}

type dayDir struct {
	path string
	day  time.Time
}

// PruneOlderThan deletes day directories older than retentionDays (Asia/Shanghai calendar).
func (fs *FileStore) PruneOlderThan(retentionDays int, now time.Time, loc *time.Location) error {
	return fs.PruneRetention(retentionDays, 0, now, loc)
}

// PruneRetention drops day directories older than retentionDays, then keeps at most retentionMax
// (oldest first). Zero disables that cap. Empty year/month directories are removed afterward.
func (fs *FileStore) PruneRetention(retentionDays, retentionMax int, now time.Time, loc *time.Location) error {
	if fs == nil || (retentionDays <= 0 && retentionMax <= 0) {
		return nil
	}
	if loc == nil {
		loc = beijingLoc
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dirs, err := fs.listDayDirsUnlocked(loc)
	if err != nil || len(dirs) == 0 {
		return err
	}
	keep := dirs
	if retentionDays > 0 {
		cutoff := now.In(loc).AddDate(0, 0, -retentionDays)
		cutoff = time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, loc)
		keep = make([]dayDir, 0, len(dirs))
		for _, d := range dirs {
			if d.day.Before(cutoff) {
				_ = os.RemoveAll(d.path)
				continue
			}
			keep = append(keep, d)
		}
	}
	if retentionMax > 0 && len(keep) > retentionMax {
		sort.Slice(keep, func(i, j int) bool {
			return keep[i].day.Before(keep[j].day)
		})
		drop := len(keep) - retentionMax
		for i := 0; i < drop; i++ {
			_ = os.RemoveAll(keep[i].path)
		}
	}
	fs.pruneEmptyCalendarUnlocked()
	return nil
}

func (fs *FileStore) listDayDirsUnlocked(loc *time.Location) ([]dayDir, error) {
	years, err := os.ReadDir(fs.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []dayDir
	for _, y := range years {
		if !y.IsDir() {
			continue
		}
		yp := filepath.Join(fs.Root, y.Name())
		months, err := os.ReadDir(yp)
		if err != nil {
			continue
		}
		for _, m := range months {
			if !m.IsDir() {
				continue
			}
			mp := filepath.Join(yp, m.Name())
			days, err := os.ReadDir(mp)
			if err != nil {
				continue
			}
			for _, d := range days {
				if !d.IsDir() {
					continue
				}
				dateStr := y.Name() + "-" + m.Name() + "-" + d.Name()
				dt, err := time.ParseInLocation("2006-01-02", dateStr, loc)
				if err != nil {
					continue
				}
				out = append(out, dayDir{path: filepath.Join(mp, d.Name()), day: dt})
			}
		}
	}
	return out, nil
}

func (fs *FileStore) pruneEmptyCalendarUnlocked() {
	years, err := os.ReadDir(fs.Root)
	if err != nil {
		return
	}
	for _, y := range years {
		if !y.IsDir() {
			continue
		}
		yp := filepath.Join(fs.Root, y.Name())
		months, err := os.ReadDir(yp)
		if err != nil {
			continue
		}
		for _, m := range months {
			if !m.IsDir() {
				continue
			}
			mp := filepath.Join(yp, m.Name())
			if left, _ := os.ReadDir(mp); len(left) == 0 {
				_ = os.Remove(mp)
			}
		}
		if left, _ := os.ReadDir(yp); len(left) == 0 {
			_ = os.Remove(yp)
		}
	}
}

// SeedRoundFromSQLiteForDay dumps current SQLite snapshots into the given calendar day's round dir
// (fallback when archiving a day that has no dumps yet).
func (fs *FileStore) SeedRoundFromSQLiteForDay(st *store.Store, day time.Time, loc *time.Location) error {
	if st == nil || fs == nil {
		return nil
	}
	if loc == nil {
		loc = beijingLoc
	}
	clusters, err := st.ListClusters()
	if err != nil {
		return err
	}
	rows, err := st.ListInspections()
	if err != nil {
		return err
	}
	byID := inspectionMap(rows)
	stamp := time.Date(day.In(loc).Year(), day.In(loc).Month(), day.In(loc).Day(), 23, 59, 59, 0, loc).UTC()
	for _, c := range clusters {
		row, ok := byID[c.ID]
		if !ok {
			continue
		}
		r := row
		r.CheckedAt = stamp
		rep := parseReport(row.ReportJSON)
		if err := fs.DumpRound(c.Name, &r, rep.Issues, loc); err != nil {
			return err
		}
	}
	return nil
}
