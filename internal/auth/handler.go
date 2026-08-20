package auth

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"gitee.com/k8man/internal/api/respond"
)

func forceRenew(c *gin.Context) bool {
	q := strings.ToLower(strings.TrimSpace(c.Query("renew")))
	return q == "1" || q == "true" || q == "yes"
}

func (s *Service) HandleIgoKey(c *gin.Context) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		respond.Fail(c, http.StatusMethodNotAllowed, 405, "method not allowed")
		return
	}
	key, expiresAt := s.ObtainKey(forceRenew(c))
	sec := int(time.Until(expiresAt).Seconds())
	if sec < 0 {
		sec = 0
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Igo-Key-Expires", expiresAt.UTC().Format(time.RFC3339))
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	respond.OK(c, gin.H{
		"key":          key,
		"expiresAt":    expiresAt.Unix(),
		"expiresInSec": sec,
	})
}

func (s *Service) HandleLogin(c *gin.Context) {
	if c.Request.Method != http.MethodPost {
		respond.Fail(c, http.StatusMethodNotAllowed, 405, "method not allowed")
		return
	}
	ip := clientIP(c)
	if retry, locked := s.LockedUntil(ip); locked {
		writeRateLimited(c, retry)
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256)
	if err := c.ShouldBindJSON(&body); err != nil && err != io.EOF {
		respond.BadRequest(c, "bad json")
		return
	}
	key := strings.TrimSpace(body.Key)
	if !looksLikeKey(key) {
		if retry, locked := s.RecordFail(ip); locked {
			writeRateLimited(c, retry)
			return
		}
		respond.Unauthorized(c, "密钥无效或已过期")
		return
	}
	tok, exp, ok := s.IssueSession(key)
	if !ok {
		if retry, locked := s.RecordFail(ip); locked {
			writeRateLimited(c, retry)
			return
		}
		respond.Unauthorized(c, "密钥无效或已过期")
		return
	}
	s.ClearFail(ip)
	sec := int(time.Until(exp).Seconds())
	if sec < 0 {
		sec = 0
	}
	respond.OK(c, gin.H{
		"token":        tok,
		"expiresAt":    exp.Unix(),
		"expiresInSec": sec,
	})
}

func (s *Service) HandleSession(c *gin.Context) {
	if c.Request.Method != http.MethodPost {
		respond.Fail(c, http.StatusMethodNotAllowed, 405, "method not allowed")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512)
	if err := c.ShouldBindJSON(&body); err != nil && err != io.EOF {
		respond.BadRequest(c, "bad json")
		return
	}
	sec, reason, ok := s.SessionCheck(body.Token)
	if !ok {
		respond.Unauthorized(c, FailMessage(reason))
		return
	}
	respond.OK(c, gin.H{"expiresInSec": sec})
}

func writeRateLimited(c *gin.Context, retrySec int) {
	if retrySec < 1 {
		retrySec = 1
	}
	c.Header("Retry-After", strconv.Itoa(retrySec))
	respond.Fail(c, http.StatusTooManyRequests, 429, "验证尝试过于频繁，请 "+strconv.Itoa(retrySec)+" 秒后再试")
}

func clientIP(c *gin.Context) string {
	ip := strings.TrimSpace(c.ClientIP())
	if ip != "" {
		return ip
	}
	return stringsOrUnknown(c.Request.RemoteAddr)
}

func TokenFromRequest(c *gin.Context) string {
	tok := strings.TrimSpace(c.GetHeader("X-Igo-Token"))
	if tok == "" {
		tok = strings.TrimSpace(c.Query("token"))
	}
	return tok
}
