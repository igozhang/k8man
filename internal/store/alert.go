package store

import (
	"database/sql"
	"time"

	"github.com/google/uuid"

	"gitee.com/k8man/internal/model"
)

// DefaultFeishuWebhookURL is seeded when alert_webhooks is empty.
const DefaultFeishuWebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/e52319a0-09dc-40f1-8132-73d7df06af83"

func (s *Store) seedDefaultAlertWebhook() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM alert_webhooks`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := time.Now().UTC()
	row := &model.AlertWebhook{
		ID:        uuid.NewString(),
		Name:      "默认飞书机器人",
		URL:       DefaultFeishuWebhookURL,
		Note:      "首次启动自动写入，可在告警页修改或删除",
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return s.CreateAlertWebhook(row)
}

// EnsureDefaultAlertWebhook inserts the built-in Feishu hook when the table is empty.
// Call from process startup (not migrate) so unit tests opening a temp DB do not get a live webhook.
func (s *Store) EnsureDefaultAlertWebhook() error {
	if s == nil {
		return nil
	}
	return s.seedDefaultAlertWebhook()
}

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

// ListAlertSentFingerprints returns fingerprints already pushed for a cluster.
func (s *Store) ListAlertSentFingerprints(clusterID string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT fingerprint,severity FROM alert_sent WHERE cluster_id=?`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var fp, sev string
		if err := rows.Scan(&fp, &sev); err != nil {
			return nil, err
		}
		out[fp] = sev
	}
	return out, rows.Err()
}

func (s *Store) UpsertAlertSent(clusterID, fingerprint, severity string, sentAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO alert_sent(cluster_id,fingerprint,severity,sent_at) VALUES(?,?,?,?)
		 ON CONFLICT(cluster_id,fingerprint) DO UPDATE SET severity=excluded.severity, sent_at=excluded.sent_at`,
		clusterID, fingerprint, severity, fmtTime(sentAt.UTC()),
	)
	return err
}

// PruneAlertSent removes fingerprints that are no longer present for the cluster.
func (s *Store) PruneAlertSent(clusterID string, keep []string) error {
	if len(keep) == 0 {
		_, err := s.db.Exec(`DELETE FROM alert_sent WHERE cluster_id=?`, clusterID)
		return err
	}
	placeholders := make([]string, len(keep))
	args := make([]any, 0, len(keep)+1)
	args = append(args, clusterID)
	for i, fp := range keep {
		placeholders[i] = "?"
		args = append(args, fp)
	}
	q := `DELETE FROM alert_sent WHERE cluster_id=? AND fingerprint NOT IN (` + joinCSV(placeholders) + `)`
	_, err := s.db.Exec(q, args...)
	return err
}

func joinCSV(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += "," + parts[i]
	}
	return out
}
