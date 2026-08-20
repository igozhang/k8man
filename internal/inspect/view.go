package inspect

import (
	"encoding/json"
	"errors"
	"time"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/store"
)

var ErrNotFound = errors.New("inspection not found")

// PreviewIssue is a compact line for the monitor summary box.
type PreviewIssue struct {
	Severity  string `json:"severity"`
	Category  string `json:"category"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
	Since     string `json:"since,omitempty"`
}

// ListItem is one cluster's latest inspection in the summary API.
type ListItem struct {
	ClusterID   string         `json:"clusterId"`
	ClusterName string         `json:"clusterName"`
	CheckedAt   *time.Time     `json:"checkedAt,omitempty"`
	NextDue     *time.Time     `json:"nextDue,omitempty"`
	ElapsedMs   int64          `json:"elapsedMs,omitempty"`
	ProbeStatus string         `json:"probeStatus,omitempty"`
	ProbeError  string         `json:"probeError,omitempty"`
	Critical    int            `json:"critical"`
	Warning     int            `json:"warning"`
	Summary     string         `json:"summary"`
	Preview     []PreviewIssue `json:"preview"`
}

// ListResult is GET /inspections.
type ListResult struct {
	IntervalSeconds int64      `json:"intervalSeconds"`
	Items           []ListItem `json:"items"`
}

// Detail is GET /inspections/:id and POST inspect.
type Detail struct {
	ListItem
	Probe    kube.ProbeResult     `json:"probe"`
	Diagnose *kube.DiagnoseReport `json:"diagnose,omitempty"`
	Issues   []kube.Issue         `json:"issues"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func listItemFromRow(name string, row *store.InspectionRow, issues []kube.Issue) ListItem {
	item := ListItem{
		ClusterName: name,
		Preview:     []PreviewIssue{},
		Summary:     "尚未巡检",
	}
	if row == nil {
		return item
	}
	item.ClusterID = row.ClusterID
	item.CheckedAt = timePtr(row.CheckedAt)
	item.NextDue = timePtr(row.NextDue)
	item.ElapsedMs = row.ElapsedMs
	item.ProbeStatus = row.ProbeStatus
	item.ProbeError = row.ProbeError
	item.Critical = row.Critical
	item.Warning = row.Warning
	if row.Summary != "" {
		item.Summary = row.Summary
	}
	if len(issues) > 0 {
		copied := append([]kube.Issue(nil), issues...)
		sortIssues(copied)
		item.Preview = previewOf(copied, PreviewMax)
	}
	return item
}

func parseReport(raw string) Report {
	var rep Report
	if raw == "" {
		return Report{Issues: []kube.Issue{}}
	}
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return Report{Issues: []kube.Issue{}}
	}
	if rep.Issues == nil {
		rep.Issues = []kube.Issue{}
	}
	return rep
}

func detailFromRow(name string, row *store.InspectionRow) *Detail {
	if row == nil {
		return nil
	}
	rep := parseReport(row.ReportJSON)
	item := listItemFromRow(name, row, rep.Issues)
	return &Detail{
		ListItem: item,
		Probe:    rep.Probe,
		Diagnose: rep.Diagnose,
		Issues:   rep.Issues,
	}
}

func (w *Worker) List() (*ListResult, error) {
	clusters, err := w.Store.ListClusters()
	if err != nil {
		return nil, err
	}
	rows, err := w.Store.ListInspections()
	if err != nil {
		return nil, err
	}
	byID := inspectionMap(rows)
	out := &ListResult{
		IntervalSeconds: int64(Interval / time.Second),
		Items:           make([]ListItem, 0, len(clusters)),
	}
	for _, c := range clusters {
		row, ok := byID[c.ID]
		var p *store.InspectionRow
		var issues []kube.Issue
		if ok {
			p = &row
			issues = parseReport(row.ReportJSON).Issues
		}
		item := listItemFromRow(c.Name, p, issues)
		item.ClusterID = c.ID
		if item.Summary == "" {
			item.Summary = "尚未巡检"
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (w *Worker) Get(clusterID string) (*Detail, error) {
	cl, err := w.Store.GetCluster(clusterID, false)
	if err != nil {
		return nil, err
	}
	if cl == nil {
		return nil, ErrNotFound
	}
	row, err := w.Store.GetInspection(clusterID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	d := detailFromRow(cl.Name, row)
	d.ClusterID = cl.ID
	d.ClusterName = cl.Name
	return d, nil
}
