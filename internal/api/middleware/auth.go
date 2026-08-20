package middleware

import (
	"github.com/gin-gonic/gin"

	"gitee.com/k8man/internal/api/respond"
	"gitee.com/k8man/internal/auth"
)

// AuthWrite requires a valid igokey session (X-Igo-Token or ?token=).
func AuthWrite(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil {
			respond.Unauthorized(c, auth.FailMessage(auth.ReasonInvalid))
			c.Abort()
			return
		}
		tok := auth.TokenFromRequest(c)
		ok, reason := svc.CheckToken(tok)
		if !ok {
			respond.Unauthorized(c, auth.FailMessage(reason))
			c.Abort()
			return
		}
		c.Next()
	}
}
