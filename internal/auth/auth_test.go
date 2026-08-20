package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"gitee.com/k8man/internal/model"
)

func TestRandomKeyAlphabet(t *testing.T) {
	k, err := randomKey(KeyLen)
	if err != nil {
		t.Fatal(err)
	}
	if len(k) != KeyLen {
		t.Fatalf("len=%d", len(k))
	}
	if !looksLikeKey(k) {
		t.Fatalf("not letters: %q", k)
	}
}

func TestObtainKey_ReuseAndRenew(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	k1, exp1 := s.ObtainKey(false)
	k2, _ := s.ObtainKey(false)
	if k1 != k2 {
		t.Fatalf("reuse: %q vs %q", k1, k2)
	}
	if !s.ValidateKey(k1) {
		t.Fatal("k1 should be valid")
	}
	if time.Until(exp1) < 50*time.Minute {
		t.Fatalf("ttl too short: %v", time.Until(exp1))
	}

	k3, _ := s.ObtainKey(true)
	if k3 == k1 {
		t.Fatal("renew must issue a new key")
	}
	if s.ValidateKey(k1) {
		t.Fatal("old key must be revoked")
	}
	if !s.ValidateKey(k3) {
		t.Fatal("new key should be valid")
	}

	s2 := New(dir)
	if !s2.ValidateKey(k3) {
		t.Fatal("active key should persist across restart")
	}
}

func TestObtainKey_RevokesSessions(t *testing.T) {
	s := New(t.TempDir())
	k1, _ := s.ObtainKey(false)
	tok, _, ok := s.IssueSession(k1)
	if !ok || tok == "" {
		t.Fatal("issue session")
	}
	if !s.TokenOK(tok) {
		t.Fatal("token should be ok")
	}
	_, _ = s.ObtainKey(true)
	if s.TokenOK(tok) {
		t.Fatal("token must die after key rotate")
	}
	if s.TokenFailReason(tok) != ReasonKeyRevoked {
		t.Fatalf("reason=%s", s.TokenFailReason(tok))
	}
}

func TestValidateKey_RejectsGarbage(t *testing.T) {
	s := New(t.TempDir())
	if s.ValidateKey("short") || s.ValidateKey("12345678") || s.ValidateKey("") {
		t.Fatal("garbage accepted")
	}
}

func TestLoginAndSessionHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := New(t.TempDir())
	key, _ := s.ObtainKey(false)

	r := gin.New()
	r.GET("/api/v1/auth/igokey", s.HandleIgoKey)
	r.POST("/api/v1/auth/login", s.HandleLogin)
	r.POST("/api/v1/auth/session", s.HandleSession)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/igokey", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("igokey status=%d %s", rec.Code, rec.Body.String())
	}
	var keyWrap model.APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &keyWrap); err != nil {
		t.Fatal(err)
	}
	if keyWrap.Code != 0 {
		t.Fatalf("igokey code=%d", keyWrap.Code)
	}

	body, _ := json.Marshal(map[string]string{"key": key})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:1"
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d %s", rec.Code, rec.Body.String())
	}
	var loginWrap struct {
		Code int `json:"code"`
		Data struct {
			Token        string `json:"token"`
			ExpiresInSec int    `json:"expiresInSec"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &loginWrap); err != nil {
		t.Fatal(err)
	}
	if loginWrap.Data.Token == "" || loginWrap.Data.ExpiresInSec <= 0 {
		t.Fatalf("login data=%+v", loginWrap.Data)
	}

	sessBody, _ := json.Marshal(map[string]string{"token": loginWrap.Data.Token})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", bytes.NewReader(sessBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session status=%d %s", rec.Code, rec.Body.String())
	}

	bad, _ := json.Marshal(map[string]string{"key": "badkeyzz"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(bad))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:1"
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key status=%d", rec.Code)
	}
}

func TestAuthFailRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	s := New(dir)
	key, _ := s.ObtainKey(false)

	r := gin.New()
	r.POST("/login", s.HandleLogin)

	do := func(want string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"key": want})
		req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.9:1"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 4; i++ {
		rec := do("badkeyzz")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("fail %d status=%d", i+1, rec.Code)
		}
	}
	rec := do("badkeyzz")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("5th fail status=%d want 429", rec.Code)
	}
	recOK := do(key)
	if recOK.Code != http.StatusTooManyRequests {
		t.Fatalf("during lockout status=%d", recOK.Code)
	}

	s.rlMu.Lock()
	if st := s.rl["203.0.113.9"]; st != nil {
		st.lockedUntil = time.Now().Add(-time.Second)
	}
	s.persistFailRLLocked()
	s.rlMu.Unlock()

	rec2 := do(key)
	if rec2.Code != http.StatusOK {
		t.Fatalf("after lock expired status=%d %s", rec2.Code, rec2.Body.String())
	}

	if _, err := os.Stat(filepath.Join(dir, authRLFile)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestFailMessage(t *testing.T) {
	if !strings.Contains(FailMessage(ReasonKeyRevoked), "旧 key") {
		t.Fatal(FailMessage(ReasonKeyRevoked))
	}
}
