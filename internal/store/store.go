package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("mkdir data: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping() error {
	return s.db.Ping()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS clusters (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  kubeconfig TEXT NOT NULL,
  perm_type TEXT NOT NULL DEFAULT 'unknown',
  namespaces TEXT NOT NULL DEFAULT '[]',
  server TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'unknown',
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS docs (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  slug TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT 'general',
  tags TEXT NOT NULL DEFAULT '[]',
  summary TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  pinned INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_docs_slug ON docs(slug) WHERE slug != '';
CREATE TABLE IF NOT EXISTS host_aliases (
  id TEXT PRIMARY KEY,
  hostname TEXT NOT NULL UNIQUE,
  ip TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE clusters ADD COLUMN front_proxy TEXT NOT NULL DEFAULT ''`)
	return nil
}

func encodeStrings(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func decodeStrings(s string) []string {
	var out []string
	if s == "" {
		return []string{}
	}
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		return []string{}
	}
	return out
}

func fmtTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

// ---- clusters ----

func (s *Store) ListClusters() ([]model.Cluster, error) {
	rows, err := s.db.Query(`SELECT id,name,description,kubeconfig,perm_type,namespaces,server,status,last_error,front_proxy,created_at,updated_at FROM clusters ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.Cluster
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		c.InsecureSkipTLS = kube.KubeconfigInsecureSkipTLS(c.Kubeconfig)
		c.Kubeconfig = "" // hide in list
		list = append(list, c)
	}
	if list == nil {
		list = []model.Cluster{}
	}
	return list, rows.Err()
}

func (s *Store) GetCluster(id string, withKubeconfig bool) (*model.Cluster, error) {
	row := s.db.QueryRow(`SELECT id,name,description,kubeconfig,perm_type,namespaces,server,status,last_error,front_proxy,created_at,updated_at FROM clusters WHERE id=?`, id)
	c, err := scanCluster(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !withKubeconfig {
		c.InsecureSkipTLS = kube.KubeconfigInsecureSkipTLS(c.Kubeconfig)
		c.Kubeconfig = ""
	} else {
		c.InsecureSkipTLS = kube.KubeconfigInsecureSkipTLS(c.Kubeconfig)
	}
	return &c, nil
}

func (s *Store) GetClusterByName(name string) (*model.Cluster, error) {
	row := s.db.QueryRow(`SELECT id,name,description,kubeconfig,perm_type,namespaces,server,status,last_error,front_proxy,created_at,updated_at FROM clusters WHERE name=?`, name)
	c, err := scanCluster(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) CreateCluster(c *model.Cluster) error {
	_, err := s.db.Exec(`INSERT INTO clusters(id,name,description,kubeconfig,perm_type,namespaces,server,status,last_error,front_proxy,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.Description, c.Kubeconfig, c.PermType, encodeStrings(c.Namespaces), c.Server, c.Status, c.LastError, c.FrontProxy, fmtTime(c.CreatedAt), fmtTime(c.UpdatedAt))
	return err
}

func (s *Store) UpdateCluster(c *model.Cluster) error {
	_, err := s.db.Exec(`UPDATE clusters SET name=?,description=?,kubeconfig=?,perm_type=?,namespaces=?,server=?,status=?,last_error=?,front_proxy=?,updated_at=? WHERE id=?`,
		c.Name, c.Description, c.Kubeconfig, c.PermType, encodeStrings(c.Namespaces), c.Server, c.Status, c.LastError, c.FrontProxy, fmtTime(c.UpdatedAt), c.ID)
	return err
}

func (s *Store) DeleteCluster(id string) error {
	res, err := s.db.Exec(`DELETE FROM clusters WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCluster(r rowScanner) (model.Cluster, error) {
	var c model.Cluster
	var ns, created, updated string
	err := r.Scan(&c.ID, &c.Name, &c.Description, &c.Kubeconfig, &c.PermType, &ns, &c.Server, &c.Status, &c.LastError, &c.FrontProxy, &created, &updated)
	if err != nil {
		return c, err
	}
	c.Namespaces = decodeStrings(ns)
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(updated)
	return c, nil
}

// ---- docs ----

func (s *Store) ListDocs(category, q string) ([]model.Doc, error) {
	query := `SELECT id,title,slug,category,tags,summary,content,pinned,created_at,updated_at FROM docs WHERE 1=1`
	args := []any{}
	if category != "" {
		query += ` AND category=?`
		args = append(args, category)
	}
	if q != "" {
		query += ` AND (title LIKE ? OR summary LIKE ?)`
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	query += ` ORDER BY pinned DESC, updated_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.Doc
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		d.Content = "" // list without full body
		list = append(list, d)
	}
	if list == nil {
		list = []model.Doc{}
	}
	return list, rows.Err()
}

func (s *Store) GetDoc(id string) (*model.Doc, error) {
	row := s.db.QueryRow(`SELECT id,title,slug,category,tags,summary,content,pinned,created_at,updated_at FROM docs WHERE id=?`, id)
	d, err := scanDoc(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) CreateDoc(d *model.Doc) error {
	_, err := s.db.Exec(`INSERT INTO docs(id,title,slug,category,tags,summary,content,pinned,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Title, d.Slug, d.Category, encodeStrings(d.Tags), d.Summary, d.Content, boolToInt(d.Pinned), fmtTime(d.CreatedAt), fmtTime(d.UpdatedAt))
	return err
}

func (s *Store) UpdateDoc(d *model.Doc) error {
	_, err := s.db.Exec(`UPDATE docs SET title=?,slug=?,category=?,tags=?,summary=?,content=?,pinned=?,updated_at=? WHERE id=?`,
		d.Title, d.Slug, d.Category, encodeStrings(d.Tags), d.Summary, d.Content, boolToInt(d.Pinned), fmtTime(d.UpdatedAt), d.ID)
	return err
}

func (s *Store) DeleteDoc(id string) error {
	res, err := s.db.Exec(`DELETE FROM docs WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanDoc(r rowScanner) (model.Doc, error) {
	var d model.Doc
	var tags, created, updated string
	var pinned int
	err := r.Scan(&d.ID, &d.Title, &d.Slug, &d.Category, &tags, &d.Summary, &d.Content, &pinned, &created, &updated)
	if err != nil {
		return d, err
	}
	d.Tags = decodeStrings(tags)
	d.Pinned = pinned != 0
	d.CreatedAt = parseTime(created)
	d.UpdatedAt = parseTime(updated)
	return d, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func IsUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique")
}

// ---- host aliases ----

func (s *Store) ListHostAliases() ([]model.HostAlias, error) {
	rows, err := s.db.Query(`SELECT id,hostname,ip,note,created_at,updated_at FROM host_aliases ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []model.HostAlias
	for rows.Next() {
		h, err := scanHostAlias(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, h)
	}
	if list == nil {
		list = []model.HostAlias{}
	}
	return list, rows.Err()
}

func (s *Store) GetHostAlias(id string) (*model.HostAlias, error) {
	row := s.db.QueryRow(`SELECT id,hostname,ip,note,created_at,updated_at FROM host_aliases WHERE id=?`, id)
	h, err := scanHostAlias(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *Store) CreateHostAlias(h *model.HostAlias) error {
	_, err := s.db.Exec(`INSERT INTO host_aliases(id,hostname,ip,note,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		h.ID, h.Hostname, h.IP, h.Note, fmtTime(h.CreatedAt), fmtTime(h.UpdatedAt))
	return err
}

func (s *Store) UpdateHostAlias(h *model.HostAlias) error {
	_, err := s.db.Exec(`UPDATE host_aliases SET hostname=?,ip=?,note=?,updated_at=? WHERE id=?`,
		h.Hostname, h.IP, h.Note, fmtTime(h.UpdatedAt), h.ID)
	return err
}

func (s *Store) DeleteHostAlias(id string) error {
	res, err := s.db.Exec(`DELETE FROM host_aliases WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) HostAliasMap() (map[string]string, error) {
	list, err := s.ListHostAliases()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, h := range list {
		out[strings.ToLower(h.Hostname)] = h.IP
	}
	return out, nil
}

func scanHostAlias(r rowScanner) (model.HostAlias, error) {
	var h model.HostAlias
	var created, updated string
	err := r.Scan(&h.ID, &h.Hostname, &h.IP, &h.Note, &created, &updated)
	if err != nil {
		return h, err
	}
	h.CreatedAt = parseTime(created)
	h.UpdatedAt = parseTime(updated)
	return h, nil
}
