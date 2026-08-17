package hostsfile

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitee.com/k8man/internal/kube"
	"gitee.com/k8man/internal/model"
)

const fileName = "hosts"

const defaultHeader = `# k8man hosts — IP hostname（空格分隔）
# 可直接编辑本文件，也可在页面「Hosts 管理」中修改；保存后自动生效。
#

`

// File is the source of truth for private host resolution under etc/hosts.
type File struct {
	path     string
	mu       sync.Mutex
	resolver *kube.HostsResolver
	onChange func()

	lastMod  time.Time
	lastSize int64
}

func New(etcDir string, resolver *kube.HostsResolver, onChange func()) *File {
	return &File{
		path:     filepath.Join(etcDir, fileName),
		resolver: resolver,
		onChange: onChange,
	}
}

func (f *File) Path() string { return f.path }

func (f *File) Ensure() error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(f.path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(f.path, []byte(defaultHeader), 0o644)
}

type lineKind int

const (
	lineBlank lineKind = iota
	lineComment
	lineEntry
)

type fileLine struct {
	kind     lineKind
	raw      string
	ip       string
	hostname string
}

func (f *File) readLines() ([]fileLine, error) {
	if err := f.Ensure(); err != nil {
		return nil, err
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	var out []fileLine
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		raw := sc.Text()
		trim := strings.TrimSpace(raw)
		if trim == "" {
			out = append(out, fileLine{kind: lineBlank, raw: raw})
			continue
		}
		if strings.HasPrefix(trim, "#") {
			out = append(out, fileLine{kind: lineComment, raw: raw})
			continue
		}
		parts := strings.Fields(trim)
		if len(parts) < 2 {
			out = append(out, fileLine{kind: lineComment, raw: raw})
			continue
		}
		out = append(out, fileLine{
			kind:     lineEntry,
			raw:      raw,
			ip:       parts[0],
			hostname: strings.ToLower(parts[1]),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *File) writeLines(lines []fileLine) error {
	var b strings.Builder
	for _, ln := range lines {
		switch ln.kind {
		case lineEntry:
			b.WriteString(ln.ip)
			b.WriteByte(' ')
			b.WriteString(ln.hostname)
		default:
			b.WriteString(ln.raw)
		}
		b.WriteByte('\n')
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *File) applyResolver(lines []fileLine) {
	m := make(map[string]string)
	for _, ln := range lines {
		if ln.kind == lineEntry {
			m[ln.hostname] = ln.ip
		}
	}
	if f.resolver != nil {
		f.resolver.Replace(m)
	}
	if f.onChange != nil {
		f.onChange()
	}
	st, err := os.Stat(f.path)
	if err == nil {
		f.lastMod = st.ModTime()
		f.lastSize = st.Size()
	}
}

// Reload reads etc/hosts into the resolver.
func (f *File) Reload() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := f.readLines()
	if err != nil {
		return err
	}
	f.applyResolver(lines)
	return nil
}

// SyncIfChanged reloads when the file mtime/size changed (e.g. manual edit).
func (f *File) SyncIfChanged() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.Ensure(); err != nil {
		return false, err
	}
	st, err := os.Stat(f.path)
	if err != nil {
		return false, err
	}
	if st.ModTime().Equal(f.lastMod) && st.Size() == f.lastSize {
		return false, nil
	}
	lines, err := f.readLines()
	if err != nil {
		return false, err
	}
	f.applyResolver(lines)
	return true, nil
}

func (f *File) List() ([]model.HostAlias, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := f.readLines()
	if err != nil {
		return nil, err
	}
	f.applyResolver(lines)
	var list []model.HostAlias
	for _, ln := range lines {
		if ln.kind != lineEntry {
			continue
		}
		list = append(list, model.HostAlias{
			ID:       ln.hostname,
			Hostname: ln.hostname,
			IP:       ln.ip,
		})
	}
	if list == nil {
		list = []model.HostAlias{}
	}
	return list, nil
}

func Validate(hostname, ip string) error {
	hostname = strings.TrimSpace(hostname)
	ip = strings.TrimSpace(ip)
	if hostname == "" {
		return fmt.Errorf("hostname required")
	}
	if strings.ContainsAny(hostname, " \t/") || strings.Contains(hostname, ":") {
		return fmt.Errorf("invalid hostname")
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid ip")
	}
	return nil
}

func (f *File) Create(hostname, ip string) (*model.HostAlias, error) {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	ip = strings.TrimSpace(ip)
	if err := Validate(hostname, ip); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := f.readLines()
	if err != nil {
		return nil, err
	}
	for _, ln := range lines {
		if ln.kind == lineEntry && ln.hostname == hostname {
			return nil, fmt.Errorf("hostname already exists")
		}
	}
	lines = append(lines, fileLine{kind: lineEntry, ip: ip, hostname: hostname})
	if err := f.writeLines(lines); err != nil {
		return nil, err
	}
	f.applyResolver(lines)
	return &model.HostAlias{ID: hostname, Hostname: hostname, IP: ip}, nil
}

func (f *File) Update(id, hostname, ip string) (*model.HostAlias, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	ip = strings.TrimSpace(ip)
	if err := Validate(hostname, ip); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := f.readLines()
	if err != nil {
		return nil, err
	}
	found := false
	for i := range lines {
		if lines[i].kind != lineEntry {
			continue
		}
		if lines[i].hostname == id {
			lines[i].hostname = hostname
			lines[i].ip = ip
			found = true
			continue
		}
		if lines[i].hostname == hostname && id != hostname {
			return nil, fmt.Errorf("hostname already exists")
		}
	}
	if !found {
		return nil, os.ErrNotExist
	}
	if err := f.writeLines(lines); err != nil {
		return nil, err
	}
	f.applyResolver(lines)
	return &model.HostAlias{ID: hostname, Hostname: hostname, IP: ip}, nil
}

func (f *File) Delete(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := f.readLines()
	if err != nil {
		return err
	}
	next := make([]fileLine, 0, len(lines))
	found := false
	for _, ln := range lines {
		if ln.kind == lineEntry && ln.hostname == id {
			found = true
			continue
		}
		next = append(next, ln)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := f.writeLines(next); err != nil {
		return err
	}
	f.applyResolver(next)
	return nil
}

// SeedFromMap writes entries when the file has no host lines yet (e.g. DB migration).
func (f *File) SeedFromMap(entries map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines, err := f.readLines()
	if err != nil {
		return err
	}
	for _, ln := range lines {
		if ln.kind == lineEntry {
			f.applyResolver(lines)
			return nil // already has data
		}
	}
	for host, ip := range entries {
		host = strings.ToLower(strings.TrimSpace(host))
		ip = strings.TrimSpace(ip)
		if Validate(host, ip) != nil {
			continue
		}
		lines = append(lines, fileLine{kind: lineEntry, ip: ip, hostname: host})
	}
	if err := f.writeLines(lines); err != nil {
		return err
	}
	f.applyResolver(lines)
	return nil
}

// Watch polls the hosts file and reloads when it changes on disk.
func (f *File) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = f.SyncIfChanged()
		}
	}
}
