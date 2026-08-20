package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"gitee.com/k8man/internal/api/respond"
)

func TestAuthWriteGin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := New(t.TempDir())
	key, _ := s.ObtainKey(false)
	tok, _, ok := s.IssueSession(key)
	if !ok {
		t.Fatal("session")
	}

	r := gin.New()
	r.POST("/write/ping", func(c *gin.Context) {
		if !s.TokenOK(TokenFromRequest(c)) {
			respond.Unauthorized(c, FailMessage(s.TokenFailReason(TokenFromRequest(c))))
			c.Abort()
			return
		}
		respond.OK(c, gin.H{"ok": true})
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/write/ping", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token status=%d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/write/ping", nil)
	req.Header.Set("X-Igo-Token", tok)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with token status=%d %s", rec.Code, rec.Body.String())
	}

	var wrap struct {
		Code int `json:"code"`
		Data struct {
			OK bool `json:"ok"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wrap); err != nil {
		t.Fatal(err)
	}
	if wrap.Code != 0 || !wrap.Data.OK {
		t.Fatalf("%+v", wrap)
	}
}
