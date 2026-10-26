package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"my-agent/internal/auth"
)

func requireAuth(authService *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.Fields(c.GetHeader("Authorization"))

		if len(parts) != 2 ||
			!strings.EqualFold(parts[0], "Bearer") {
			c.Abort()

			writeError(
				c,
				http.StatusUnauthorized,
				"UNAUTHORIZED",
				"请先登录",
				nil,
			)
			return
		}

		token := parts[1]

		userID, err := authService.Authenticate(token)
		if err != nil || userID == "" {
			c.Abort()

			writeError(
				c,
				http.StatusUnauthorized,
				"INVALID_TOKEN",
				"登录已失效，请重新登录",
				nil,
			)
			return
		}

		c.Set("userID", userID)
		c.Set("authToken", token)

		c.Next()
	}
}
