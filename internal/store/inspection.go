package store

import (
	"database/sql"
	"time"
)

// InspectionRow is the latest patrol snapshot for one cluster.
type InspectionRow struct {
	ClusterID   string
	CheckedAt   time.Time
	NextDue     time.Time
	ElapsedMs   int64
	ProbeStatus string
	ProbeError  string
	Critical    int
	Error       int
	Warning     int
	Info        int
	Summary     string
	ReportJSON  string
}

func (s *Store) UpsertInspection(row *InspectionRow) error {
	_, err := s.db.Exec(`INSERT INTO cluster_inspections(
  cluster_id,checked_at,next_due,elapsed_ms,probe_status,probe_error,critical,error,warning,info,summary,report_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(cluster_id) DO UPDATE SET
  checked_at=excluded.checked_at,
  next_due=excluded.next_due,
  elapsed_ms=excluded.elapsed_ms,
  probe_status=excluded.probe_status,
  probe_error=excluded.probe_error,
  critical=excluded.critical,
  error=excluded.error,
  warning=excluded.warning,
  info=excluded.info,
  summary=excluded.summary,
  report_json=excluded.report_json`,
		row.ClusterID, fmtTime(row.CheckedAt), fmtTime(row.NextDue), row.ElapsedMs,
		row.ProbeStatus, row.ProbeError, row.Critical, row.Error, row.Warning, row.Info, row.Summary, row.ReportJSON)
	return err
}

func (s *Store) GetInspection(clusterID string) (*InspectionRow, error) {
	row := s.db.QueryRow(`SELECT cluster_id,checked_at,next_due,elapsed_ms,probe_status,probe_error,critical,error,warning,info,summary,report_json
FROM cluster_inspections WHERE cluster_id=?`, clusterID)
	out, err := scanInspection(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) ListInspections() ([]InspectionRow, error) {
	rows, err := s.db.Query(`SELECT cluster_id,checked_at,next_due,elapsed_ms,probe_status,probe_error,critical,error,warning,info,summary,report_json
FROM cluster_inspections`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []InspectionRow
	for rows.Next() {
		item, err := scanInspection(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	if list == nil {
		list = []InspectionRow{}
	}
	return list, rows.Err()
}

func (s *Store) DeleteInspection(clusterID string) error {
	_, err := s.db.Exec(`DELETE FROM cluster_inspections WHERE cluster_id=?`, clusterID)
	return err
}

func scanInspection(r rowScanner) (InspectionRow, error) {
	var out InspectionRow
	var checked, due string
	err := r.Scan(&out.ClusterID, &checked, &due, &out.ElapsedMs, &out.ProbeStatus, &out.ProbeError,
		&out.Critical, &out.Error, &out.Warning, &out.Info, &out.Summary, &out.ReportJSON)
	if err != nil {
		return out, err
	}
	if checked != "" {
		out.CheckedAt = parseTime(checked)
	}
	if due != "" {
		out.NextDue = parseTime(due)
	}
	return out, nil
}
