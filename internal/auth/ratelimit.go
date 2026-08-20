package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	failMax       = 5
	failWindow    = time.Minute
	failLockAfter = time.Minute
)

type failDiskEntry struct {
	FailAt      []int64 `json:"failAt,omitempty"`
	LockedUntil int64   `json:"lockedUntil,omitempty"`
}

type failState struct {
	failTimes   []time.Time
	lockedUntil time.Time
}

func (s *Service) rlPath() string {
	if s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, authRLFile)
}

func (s *Service) loadFailRL() {
	s.rlMu.Lock()
	defer s.rlMu.Unlock()
	s.loadFailRLLocked()
}

func (s *Service) loadFailRLLocked() {
	path := s.rlPath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var disk map[string]failDiskEntry
	if err := json.Unmarshal(raw, &disk); err != nil {
		return
	}
	now := time.Now()
	cutoff := now.Add(-failWindow).Unix()
	for ip, e := range disk {
		st := &failState{}
		if e.LockedUntil > 0 {
			st.lockedUntil = time.Unix(e.LockedUntil, 0)
		}
		for _, ts := range e.FailAt {
			if ts >= cutoff {
				st.failTimes = append(st.failTimes, time.Unix(ts, 0))
			}
		}
		if st.lockedUntil.After(now) || len(st.failTimes) > 0 {
			s.rl[ip] = st
		}
	}
}

func (s *Service) persistFailRLLocked() {
	path := s.rlPath()
	if path == "" {
		return
	}
	now := time.Now()
	cutoff := now.Add(-failWindow).Unix()
	disk := make(map[string]failDiskEntry, len(s.rl))
	for ip, st := range s.rl {
		if st == nil {
			continue
		}
		e := failDiskEntry{}
		if st.lockedUntil.After(now) {
			e.LockedUntil = st.lockedUntil.Unix()
		}
		for _, t := range st.failTimes {
			if ts := t.Unix(); ts >= cutoff {
				e.FailAt = append(e.FailAt, ts)
			}
		}
		if e.LockedUntil > 0 || len(e.FailAt) > 0 {
			disk[ip] = e
		}
	}
	if len(disk) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = atomicWriteJSON(path, disk, 0o600)
}

// LockedUntil returns remaining lock seconds if this IP is currently locked.
func (s *Service) LockedUntil(ip string) (retrySec int, locked bool) {
	ip = stringsOrUnknown(ip)
	now := time.Now()
	s.rlMu.Lock()
	defer s.rlMu.Unlock()
	st := s.rl[ip]
	if st != nil && !st.lockedUntil.IsZero() && now.Before(st.lockedUntil) {
		sec := int(time.Until(st.lockedUntil).Seconds())
		if sec < 1 {
			sec = 1
		}
		return sec, true
	}
	if st != nil && !st.lockedUntil.IsZero() && !now.Before(st.lockedUntil) {
		delete(s.rl, ip)
	}
	return 0, false
}

func (s *Service) RecordFail(ip string) (retrySec int, locked bool) {
	ip = stringsOrUnknown(ip)
	now := time.Now()
	cutoff := now.Add(-failWindow)
	s.rlMu.Lock()
	defer s.rlMu.Unlock()
	st := s.rl[ip]
	if st == nil {
		st = &failState{}
		s.rl[ip] = st
	}
	ft := make([]time.Time, 0, len(st.failTimes)+1)
	for _, t := range st.failTimes {
		if t.After(cutoff) {
			ft = append(ft, t)
		}
	}
	ft = append(ft, now)
	st.failTimes = ft
	if len(st.failTimes) >= failMax {
		st.lockedUntil = now.Add(failLockAfter)
		st.failTimes = nil
		s.persistFailRLLocked()
		return int(failLockAfter.Seconds()), true
	}
	s.persistFailRLLocked()
	return 0, false
}

func (s *Service) ClearFail(ip string) {
	ip = stringsOrUnknown(ip)
	s.rlMu.Lock()
	delete(s.rl, ip)
	s.persistFailRLLocked()
	s.rlMu.Unlock()
}

func stringsOrUnknown(ip string) string {
	if ip == "" {
		return "unknown"
	}
	return ip
}
