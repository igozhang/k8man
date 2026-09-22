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
	Error       int            `json:"error"`
	Warning     int            `json:"warning"`
	Info        int            `json:"info"`
	Summary     string         `json:"summary"`
	Preview     []PreviewIssue `json:"preview"`
	// HasScan / ScanPods / PodIssues come from the latest DiagnoseReport (overview cards).
	HasScan   bool `json:"hasScan"`
	ScanPods  int  `json:"scanPods"`
	PodIssues int  `json:"podIssues"`
	Truncated bool `json:"truncated,omitempty"`
	// Storage summary for overview: large ephemeral pods + default/unique NFS SC usage.
	LargePods         int      `json:"largePods"`
	LargePodSkipped   string   `json:"largePodSkipped,omitempty"`
	NFSUsagePercent   *float64 `json:"nfsUsagePercent,omitempty"`
	NFSStorageClass   string   `json:"nfsStorageClass,omitempty"`
	NFSServer         string   `json:"nfsServer,omitempty"`
	NFSPath           string   `json:"nfsPath,omitempty"`
	NFSSource         string   `json:"nfsSource,omitempty"`
	NFSSkipped        string   `json:"nfsSkipped,omitempty"`
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

func listItemFromRow(name string, row *store.InspectionRow, issues []kube.Issue, diag *kube.DiagnoseReport) ListItem {
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
	item.Error = row.Error
	item.Warning = row.Warning
	item.Info = row.Info
	if row.Summary != "" {
		item.Summary = row.Summary
	}
	if len(issues) > 0 {
		copied := append([]kube.Issue(nil), issues...)
		sortIssues(copied)
		item.Preview = previewOf(copied, PreviewMax)
	}
	if diag != nil {
		item.HasScan = true
		item.ScanPods = diag.Scan.Pods
		item.PodIssues = diag.Counts.Pods
		item.Truncated = diag.Truncated
		item.LargePods = diag.Scan.LargePods
		item.LargePodSkipped = diag.Scan.LargePodSkipped
		item.NFSUsagePercent = diag.Scan.NFSUsagePercent
		item.NFSStorageClass = diag.Scan.NFSStorageClass
		item.NFSServer = diag.Scan.NFSServer
		item.NFSPath = diag.Scan.NFSPath
		item.NFSSource = diag.Scan.NFSSource
		item.NFSSkipped = diag.Scan.NFSSkipped
		// Prefer scan.largePods; if older reports lack the field, count from issues.
		if item.LargePods == 0 && item.LargePodSkipped == "" {
			for _, it := range issues {
				if it.Category == "storage" && it.Reason == "LargeEphemeral" {
					item.LargePods++
				}
			}
		}
	} else {
		// Probe-only / diagnose failed: still surface pod issues from the issue list.
		for _, it := range issues {
			if it.Category == "pod" {
				item.PodIssues++
			}
			if it.Category == "storage" && it.Reason == "LargeEphemeral" {
				item.LargePods++
			}
		}
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
	item := listItemFromRow(name, row, rep.Issues, rep.Diagnose)
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
		var diag *kube.DiagnoseReport
		if ok {
			p = &row
			rep := parseReport(row.ReportJSON)
			issues = rep.Issues
			diag = rep.Diagnose
		}
		item := listItemFromRow(c.Name, p, issues, diag)
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
