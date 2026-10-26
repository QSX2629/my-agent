package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"my-agent/internal/agent"
	"my-agent/internal/auth"
	"my-agent/internal/store"
)

type authRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type chatRequest struct {
	SessionID string `json:"sessionId"`
	Message   string `json:"message"`
}

type chatResponse struct {
	SessionID string `json:"sessionId"`
	Reply     string `json:"reply"`
}

func NewRouter(
	agentService *agent.Service,
	messageStore *store.Store,
	authService *auth.Service,
) *gin.Engine {
	router := gin.New()

	router.Use(
		requestLogMiddleware(),
		recoveryMiddleware(),
	)

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
		})
	})

	router.POST("/api/auth/register", func(c *gin.Context) {
		var request authRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(
				c, http.StatusBadRequest,
				"INVALID_JSON", "请求格式错误", nil,
			)
			return
		}

		if err := authService.Register(
			request.Email,
			request.Password,
		); err != nil {
			writeError(
				c, http.StatusBadRequest,
				"REGISTRATION_FAILED",
				"注册失败，请检查输入或邮箱是否已注册",
				nil,
			)
			return
		}

		c.Status(http.StatusCreated)
	})

	router.POST("/api/auth/login", func(c *gin.Context) {
		var request authRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(
				c, http.StatusBadRequest,
				"INVALID_JSON", "请求格式错误", nil,
			)
			return
		}

		token, err := authService.Login(
			request.Email,
			request.Password,
		)
		if err != nil {
			writeError(
				c, http.StatusUnauthorized,
				"INVALID_CREDENTIALS",
				"登录失败，请检查邮箱和密码",
				nil,
			)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"token": token,
		})
	})

	protected := router.Group("/api")
	protected.Use(requireAuth(authService))

	registerPromptRoutes(protected, messageStore)
	registerSkillRoutes(protected, messageStore)

	protected.GET("/auth/me", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"userId": c.GetString("userID"),
		})
	})

	protected.POST("/auth/logout", func(c *gin.Context) {
		if err := authService.Logout(
			c.GetString("authToken"),
		); err != nil {
			writeError(
				c, http.StatusInternalServerError,
				"LOGOUT_FAILED", "退出登录失败", err,
			)
			return
		}

		c.Status(http.StatusNoContent)
	})

	protected.POST("/sessions", func(c *gin.Context) {
		session, err := messageStore.CreateOwnedSession(
			c.Request.Context(),
			c.GetString("userID"),
			uuid.New().String(),
			"新会话",
		)
		if err != nil {
			writeError(
				c, http.StatusInternalServerError,
				"SESSION_CREATE_FAILED", "创建会话失败", err,
			)
			return
		}

		c.JSON(http.StatusCreated, session)
	})

	protected.GET("/sessions", listSessionsHandler(messageStore))

	protected.GET("/sessions/:id/messages", listMessagesHandler(messageStore))

	protected.PATCH("/sessions/:id", renameSessionHandler(messageStore))

	protected.DELETE("/sessions/:id", func(c *gin.Context) {
		err := messageStore.DeleteOwnedSession(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("id"),
		)
		if err != nil {
			writeStoreError(c, err)
			return
		}

		c.Status(http.StatusNoContent)
	})

	protected.POST("/chat", func(c *gin.Context) {
		var request chatRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(
				c, http.StatusBadRequest,
				"INVALID_JSON", "请求格式错误", nil,
			)
			return
		}

		sessionID := strings.TrimSpace(request.SessionID)
		message := strings.TrimSpace(request.Message)

		if sessionID == "" || message == "" {
			writeError(
				c, http.StatusBadRequest,
				"INVALID_INPUT",
				"sessionId 和 message 不能为空",
				nil,
			)
			return
		}

		reply, err := agentService.Reply(
			c.Request.Context(),
			c.GetString("userID"),
			sessionID,
			message,
		)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				writeError(
					c, http.StatusNotFound,
					"SESSION_NOT_FOUND", "会话不存在", nil,
				)

			case errors.Is(err, agent.ErrContextTooLarge):
				writeError(
					c, http.StatusRequestEntityTooLarge,
					"CONTEXT_TOO_LARGE",
					"消息或历史超出处理范围，请缩短消息或新建会话",
					nil,
				)

			case errors.Is(err, agent.ErrInvalidSummary):
				writeError(
					c, http.StatusBadGateway,
					"SUMMARY_FAILED",
					"历史摘要生成失败，请稍后重试",
					err,
				)

			case errors.Is(err, context.DeadlineExceeded):
				writeError(
					c, http.StatusGatewayTimeout,
					"REQUEST_TIMEOUT", "处理超时，请稍后重试", err,
				)

			case errors.Is(err, agent.ErrModelCall):
				writeError(
					c, http.StatusBadGateway,
					"MODEL_CALL_FAILED", "模型调用失败", err,
				)

			default:
				writeError(
					c, http.StatusInternalServerError,
					"CHAT_FAILED", "处理聊天失败", err,
				)
			}
			return
		}

		c.JSON(http.StatusOK, chatResponse{
			SessionID: sessionID,
			Reply:     reply,
		})
	})

	return router
}

func writeStoreError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(
			c, http.StatusNotFound,
			"SESSION_NOT_FOUND", "会话不存在", nil,
		)
		return
	}

	writeError(
		c, http.StatusInternalServerError,
		"SESSION_OPERATION_FAILED", "会话操作失败", err,
	)
}
