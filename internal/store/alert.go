package store

import (
	"database/sql"
	"strings"
	"time"

	"gitee.com/k8man/internal/model"
)

func (s *Store) ListAlertWebhooks() ([]model.AlertWebhook, error) {
	rows, err := s.db.Query(`SELECT id,name,url,note,enabled,created_at,updated_at FROM alert_webhooks ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.AlertWebhook
	for rows.Next() {
		w, err := scanAlertWebhook(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, w)
	}
	if list == nil {
		list = []model.AlertWebhook{}
	}
	return list, rows.Err()
}

func (s *Store) ListEnabledAlertWebhooks() ([]model.AlertWebhook, error) {
	rows, err := s.db.Query(`SELECT id,name,url,note,enabled,created_at,updated_at FROM alert_webhooks WHERE enabled=1 ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.AlertWebhook
	for rows.Next() {
		w, err := scanAlertWebhook(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, w)
	}
	if list == nil {
		list = []model.AlertWebhook{}
	}
	return list, rows.Err()
}

func (s *Store) GetAlertWebhook(id string) (*model.AlertWebhook, error) {
	row := s.db.QueryRow(`SELECT id,name,url,note,enabled,created_at,updated_at FROM alert_webhooks WHERE id=?`, id)
	w, err := scanAlertWebhook(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *Store) CreateAlertWebhook(w *model.AlertWebhook) error {
	_, err := s.db.Exec(
		`INSERT INTO alert_webhooks(id,name,url,note,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		w.ID, w.Name, w.URL, w.Note, boolToInt(w.Enabled), fmtTime(w.CreatedAt), fmtTime(w.UpdatedAt),
	)
	return err
}

func (s *Store) UpdateAlertWebhook(w *model.AlertWebhook) error {
	_, err := s.db.Exec(
		`UPDATE alert_webhooks SET name=?,url=?,note=?,enabled=?,updated_at=? WHERE id=?`,
		w.Name, w.URL, w.Note, boolToInt(w.Enabled), fmtTime(w.UpdatedAt), w.ID,
	)
	return err
}

func (s *Store) DeleteAlertWebhook(id string) error {
	res, err := s.db.Exec(`DELETE FROM alert_webhooks WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanAlertWebhook(r rowScanner) (model.AlertWebhook, error) {
	var w model.AlertWebhook
	var enabled int
	var created, updated string
	err := r.Scan(&w.ID, &w.Name, &w.URL, &w.Note, &enabled, &created, &updated)
	if err != nil {
		return w, err
	}
	w.Enabled = enabled != 0
	w.CreatedAt = parseTime(created)
	w.UpdatedAt = parseTime(updated)
	return w, nil
}

// AlertSentRow is one successfully pushed issue, kept until a recovery notice is delivered.
type AlertSentRow struct {
	Fingerprint string
	Severity    string
	Detail      string
}

// ListAlertedClusterIDs returns clusters that still have a delivered alert waiting for recovery.
func (s *Store) ListAlertedClusterIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT cluster_id FROM alert_sent`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, rows.Err()
}

// ListAlertSent returns issues already pushed for a cluster.
func (s *Store) ListAlertSent(clusterID string) (map[string]AlertSentRow, error) {
	rows, err := s.db.Query(`SELECT fingerprint,severity,detail FROM alert_sent WHERE cluster_id=?`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AlertSentRow{}
	for rows.Next() {
		var row AlertSentRow
		if err := rows.Scan(&row.Fingerprint, &row.Severity, &row.Detail); err != nil {
			return nil, err
		}
		out[row.Fingerprint] = row
	}
	return out, rows.Err()
}

func (s *Store) UpsertAlertSent(clusterID, fingerprint, severity, detail string, sentAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO alert_sent(cluster_id,fingerprint,severity,detail,sent_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(cluster_id,fingerprint) DO UPDATE SET severity=excluded.severity, detail=excluded.detail, sent_at=excluded.sent_at`,
		clusterID, fingerprint, severity, detail, fmtTime(sentAt.UTC()),
	)
	return err
}

// DeleteAlertSent removes fingerprints after a recovery notice was delivered.
func (s *Store) DeleteAlertSent(clusterID string, fingerprints []string) error {
	for _, fp := range fingerprints {
		if _, err := s.db.Exec(`DELETE FROM alert_sent WHERE cluster_id=? AND fingerprint=?`, clusterID, fp); err != nil {
			return err
		}
	}
	return nil
}

// ClearAlertSent drops every delivered fingerprint for a cluster.
// Used when the kubeconfig is gone, so a deleted cluster is not probed again.
func (s *Store) ClearAlertSent(clusterID string) error {
	if strings.TrimSpace(clusterID) == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM alert_sent WHERE cluster_id=?`, clusterID)
	return err
}
