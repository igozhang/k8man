package respond

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"gitee.com/k8man/internal/model"
)

func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, model.APIResponse{Code: 0, Message: "ok", Data: data})
}

func Fail(c *gin.Context, httpStatus, code int, msg string) {
	c.JSON(httpStatus, model.APIResponse{Code: code, Message: msg})
}

func BadRequest(c *gin.Context, msg string) {
	Fail(c, http.StatusBadRequest, 400, msg)
}

func NotFound(c *gin.Context, msg string) {
	Fail(c, http.StatusNotFound, 404, msg)
}

func Internal(c *gin.Context, msg string) {
	Fail(c, http.StatusInternalServerError, 500, msg)
}

func Forbidden(c *gin.Context, msg string) {
	Fail(c, http.StatusForbidden, 403, msg)
}
