package auth

import (
	"crypto/rand"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"
)

const (
	KeyLen        = 8
	keyTTL        = time.Hour
	keyReuseAfter = 15 * time.Minute
	keyAlphabet   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	keyMaxEntries = 4096
	keyActiveFile = "igokey-active.json"
	authRLFile    = "auth-rl.json"
)

type keyEntry struct {
	expiresAt time.Time
}

type session struct {
	igoKey       string
	igoExpiresAt time.Time
}

// Service issues the 8-letter igokey, memory sessions, and write-token checks.
type Service struct {
	dataDir string

	mu     sync.RWMutex
	store  map[string]keyEntry
	active struct {
		key       string
		expiresAt time.Time
	}

	sessMu   sync.RWMutex
	sessions map[string]session

	rlMu sync.Mutex
	rl   map[string]*failState
}

func New(dataDir string) *Service {
	s := &Service{
		dataDir:  strings.TrimSpace(dataDir),
		store:    map[string]keyEntry{},
		sessions: map[string]session{},
		rl:       map[string]*failState{},
	}
	s.loadActiveFromDisk()
	s.loadFailRL()
	return s
}

// ObtainKey issues or reuses the site key: remaining >15min keeps it;
// remaining ≤15min or forceNew issues a new key and revokes the old one.
func (s *Service) ObtainKey(forceNew bool) (key string, expiresAt time.Time) {
	now := time.Now()
	var revokeKey string

	s.mu.Lock()
	s.syncActiveFromDiskLocked(now)
	s.pruneStoreLocked(now)

	if !forceNew {
		if k, exp, ok := s.activeReusableLocked(now); ok {
			s.mu.Unlock()
			return k, exp
		}
	}

	if s.active.key != "" {
		revokeKey = s.active.key
		delete(s.store, revokeKey)
		s.active.key = ""
		s.active.expiresAt = time.Time{}
	}

	key, expiresAt = s.issueLocked(now)
	s.active.key = key
	s.active.expiresAt = expiresAt
	s.persistActiveLocked()
	s.mu.Unlock()

	if revokeKey != "" && revokeKey != key {
		s.revokeSessionsForKey(revokeKey)
	}
	return key, expiresAt
}

func (s *Service) activeReusableLocked(now time.Time) (key string, expiresAt time.Time, ok bool) {
	if s.active.key == "" {
		return "", time.Time{}, false
	}
	e, found := s.store[s.active.key]
	if !found || !e.expiresAt.After(now) {
		return "", time.Time{}, false
	}
	if e.expiresAt.Sub(now) <= keyReuseAfter {
		return "", time.Time{}, false
	}
	s.active.expiresAt = e.expiresAt
	return s.active.key, e.expiresAt, true
}

func (s *Service) issueLocked(now time.Time) (key string, expiresAt time.Time) {
	expiresAt = now.Add(keyTTL)
	for i := 0; i < 8; i++ {
		k, err := randomKey(KeyLen)
		if err != nil {
			log.Printf("igokey: random: %v", err)
			continue
		}
		if len(s.store) >= keyMaxEntries {
			log.Printf("igokey: store full, dropping new key issuance")
			s.store[k] = keyEntry{expiresAt: expiresAt}
			return k, expiresAt
		}
		if _, exists := s.store[k]; exists {
			continue
		}
		s.store[k] = keyEntry{expiresAt: expiresAt}
		return k, expiresAt
	}
	k, _ := randomKey(KeyLen)
	s.store[k] = keyEntry{expiresAt: expiresAt}
	return k, expiresAt
}

func randomKey(n int) (string, error) {
	var b strings.Builder
	b.Grow(n)
	max := big.NewInt(int64(len(keyAlphabet)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(keyAlphabet[idx.Int64()])
	}
	return b.String(), nil
}

func (s *Service) pruneStoreLocked(now time.Time) {
	for k, e := range s.store {
		if !e.expiresAt.After(now) {
			delete(s.store, k)
			if s.active.key == k {
				s.active.key = ""
				s.active.expiresAt = time.Time{}
			}
		}
	}
}

func (s *Service) keyExpiry(key string) (time.Time, bool) {
	key = strings.TrimSpace(key)
	if len(key) != KeyLen {
		return time.Time{}, false
	}
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active.key != key {
		return time.Time{}, false
	}
	e, ok := s.store[key]
	if !ok || !e.expiresAt.After(now) {
		return time.Time{}, false
	}
	return e.expiresAt, true
}

func (s *Service) ValidateKey(key string) bool {
	key = strings.TrimSpace(key)
	if len(key) != KeyLen {
		return false
	}
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	now := time.Now()
	s.mu.Lock()
	s.syncActiveFromDiskLocked(now)
	s.mu.Unlock()

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active.key != key {
		return false
	}
	e, ok := s.store[key]
	return ok && e.expiresAt.After(now)
}

func looksLikeKey(key string) bool {
	key = strings.TrimSpace(key)
	if len(key) != KeyLen {
		return false
	}
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}
