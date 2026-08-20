package auth

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

const (
	ReasonKeyRevoked = "key_revoked"
	ReasonExpired    = "key_expired"
	ReasonInvalid    = "invalid_token"
)

type tokenState int

const (
	tokenOK tokenState = iota
	tokenMissing
	tokenExpired
	tokenKeyRevoked
)

func (s *Service) tokenStateOf(tok string) (tokenState, session) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return tokenMissing, session{}
	}
	now := time.Now()
	s.mu.Lock()
	s.syncActiveFromDiskLocked(now)
	s.mu.Unlock()

	s.sessMu.RLock()
	sess, ok := s.sessions[tok]
	s.sessMu.RUnlock()
	if !ok {
		return tokenKeyRevoked, session{}
	}
	if !sess.igoExpiresAt.After(now) {
		s.sessMu.Lock()
		delete(s.sessions, tok)
		s.sessMu.Unlock()
		return tokenExpired, sess
	}
	if !s.ValidateKey(sess.igoKey) {
		s.sessMu.Lock()
		delete(s.sessions, tok)
		s.sessMu.Unlock()
		return tokenKeyRevoked, sess
	}
	return tokenOK, sess
}

func (s *Service) CheckToken(tok string) (ok bool, reason string) {
	st, _ := s.tokenStateOf(tok)
	switch st {
	case tokenOK:
		return true, ""
	case tokenKeyRevoked:
		return false, ReasonKeyRevoked
	case tokenExpired:
		return false, ReasonExpired
	default:
		return false, ReasonInvalid
	}
}

func (s *Service) TokenOK(tok string) bool {
	ok, _ := s.CheckToken(tok)
	return ok
}

func (s *Service) TokenFailReason(tok string) string {
	_, reason := s.CheckToken(tok)
	return reason
}

func FailMessage(reason string) string {
	switch reason {
	case ReasonKeyRevoked:
		return "旧 key 不可用，请重新验证"
	case ReasonExpired:
		return "key 已过期，请重新验证"
	default:
		return "未认证，请先验证 key"
	}
}

func (s *Service) IssueSession(key string) (token string, expiresAt time.Time, ok bool) {
	s.mu.Lock()
	s.syncActiveFromDiskLocked(time.Now())
	s.mu.Unlock()

	exp, valid := s.keyExpiry(key)
	if !valid {
		return "", time.Time{}, false
	}
	tok, err := newSessionToken()
	if err != nil {
		return "", time.Time{}, false
	}
	s.sessMu.Lock()
	s.pruneSessionsLocked(time.Now())
	s.sessions[tok] = session{igoKey: key, igoExpiresAt: exp}
	s.sessMu.Unlock()
	return tok, exp, true
}

func (s *Service) SessionCheck(tok string) (expiresInSec int, reason string, ok bool) {
	st, sess := s.tokenStateOf(tok)
	if st == tokenOK {
		sec := int(time.Until(sess.igoExpiresAt).Seconds())
		if sec < 0 {
			sec = 0
		}
		return sec, "", true
	}
	return 0, s.TokenFailReason(tok), false
}

func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Service) pruneSessionsLocked(now time.Time) {
	for k, sess := range s.sessions {
		if !sess.igoExpiresAt.After(now) {
			delete(s.sessions, k)
		}
	}
}

func (s *Service) revokeSessionsForKey(igoKey string) {
	if igoKey == "" {
		return
	}
	now := time.Now()
	s.sessMu.Lock()
	for tok, sess := range s.sessions {
		if sess.igoKey == igoKey {
			delete(s.sessions, tok)
		}
	}
	s.pruneSessionsLocked(now)
	s.sessMu.Unlock()
}
