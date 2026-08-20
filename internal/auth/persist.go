package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type diskKeyState struct {
	Key       string `json:"key"`
	ExpiresAt int64  `json:"expiresAt"`
}

func (s *Service) activePath() string {
	if s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, keyActiveFile)
}

func (s *Service) loadActiveFromDisk() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncActiveFromDiskLocked(time.Now())
}

func (s *Service) syncActiveFromDiskLocked(now time.Time) {
	path := s.activePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var st diskKeyState
	if err := json.Unmarshal(raw, &st); err != nil || st.Key == "" || st.ExpiresAt <= 0 {
		return
	}
	exp := time.Unix(st.ExpiresAt, 0)
	if !exp.After(now) {
		_ = os.Remove(path)
		if s.active.key == st.Key {
			s.active.key = ""
			s.active.expiresAt = time.Time{}
		}
		delete(s.store, st.Key)
		return
	}
	s.store[st.Key] = keyEntry{expiresAt: exp}
	s.active.key = st.Key
	s.active.expiresAt = exp
}

func (s *Service) persistActiveLocked() {
	path := s.activePath()
	if path == "" {
		return
	}
	if s.active.key == "" {
		_ = os.Remove(path)
		return
	}
	st := diskKeyState{Key: s.active.key, ExpiresAt: s.active.expiresAt.Unix()}
	_ = atomicWriteJSON(path, st, 0o600)
}

func atomicWriteJSON(path string, v any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	// Windows cannot Rename over an existing file.
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.WriteFile(path, b, mode)
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
