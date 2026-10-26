package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

func writeError(
	c *gin.Context,
	status int,
	code string,
	message string,
	err error,
) {
	requestID := c.GetString("requestID")

	// 服务器错误记录内部原因，客户端得到统一提示。
	if status >= 500 && err != nil {
		slog.Error(
			"request.failed",
			"requestId", requestID,
			"code", code,
			"error", err,
		)
	}

	c.JSON(status, ErrorResponse{
		Error: ErrorDetail{
			Code:      code,
			Message:   message,
			RequestID: requestID,
		},
	})
}

func requestLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := uuid.New().String()
		startedAt := time.Now()

		c.Set("requestID", requestID)
		c.Header("X-Request-ID", requestID)

		c.Next()

		slog.Info(
			"http.request",
			"requestId", requestID,
			"method", c.Request.Method,
			"route", c.FullPath(),
			"status", c.Writer.Status(),
			"durationMs", time.Since(startedAt).Milliseconds(),
		)
	}
}

func recoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				c.Abort()

				writeError(
					c,
					http.StatusInternalServerError,
					"INTERNAL_ERROR",
					"服务器内部错误",
					fmt.Errorf("request handler panic: %v", recovered),
				)
			}
		}()

		c.Next()
	}
}
