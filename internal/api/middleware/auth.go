package middleware

import "github.com/gin-gonic/gin"

// AuthWrite is a placeholder for future write-operation authentication.
// First version is a no-op pass-through.
func AuthWrite() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
	}
}
