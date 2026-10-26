package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"my-agent/internal/store"
)

type promptRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type sessionPromptRequest struct {
	PromptID *string `json:"promptId"`
}

// registerPromptRoutes 注册提示词管理和会话选择接口。
func registerPromptRoutes(
	protected *gin.RouterGroup,
	messageStore *store.Store,
) {
	protected.POST("/prompts", func(c *gin.Context) {
		var request promptRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(
				c, http.StatusBadRequest,
				"INVALID_JSON", "请求格式错误", nil,
			)
			return
		}

		prompt, err := messageStore.CreateOwnedPrompt(
			c.Request.Context(),
			c.GetString("userID"),
			request.Name,
			request.Content,
		)
		if err != nil {
			writePromptError(c, err)
			return
		}

		c.JSON(http.StatusCreated, prompt)
	})

	protected.GET("/prompts", func(c *gin.Context) {
		prompts, err := messageStore.ListOwnedPrompts(
			c.Request.Context(),
			c.GetString("userID"),
		)
		if err != nil {
			writePromptError(c, err)
			return
		}

		c.JSON(http.StatusOK, prompts)
	})

	protected.GET("/prompts/:id", func(c *gin.Context) {
		prompt, err := messageStore.GetOwnedPrompt(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("id"),
		)
		if err != nil {
			writePromptError(c, err)
			return
		}

		c.JSON(http.StatusOK, prompt)
	})

	protected.PUT("/prompts/:id", func(c *gin.Context) {
		var request promptRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(
				c, http.StatusBadRequest,
				"INVALID_JSON", "请求格式错误", nil,
			)
			return
		}

		prompt, err := messageStore.UpdateOwnedPrompt(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("id"),
			request.Name,
			request.Content,
		)
		if err != nil {
			writePromptError(c, err)
			return
		}

		c.JSON(http.StatusOK, prompt)
	})

	protected.DELETE("/prompts/:id", func(c *gin.Context) {
		err := messageStore.DeleteOwnedPrompt(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("id"),
		)
		if err != nil {
			writePromptError(c, err)
			return
		}

		c.Status(http.StatusNoContent)
	})

	protected.GET("/sessions/:id/prompt", func(c *gin.Context) {
		selection, err := messageStore.GetSessionPrompt(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("id"),
		)
		if err != nil {
			writePromptError(c, err)
			return
		}

		c.JSON(http.StatusOK, selection)
	})

	protected.PUT("/sessions/:id/prompt", func(c *gin.Context) {
		var request sessionPromptRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(
				c, http.StatusBadRequest,
				"INVALID_JSON", "请求格式错误", nil,
			)
			return
		}

		if request.PromptID == nil {
			writeError(
				c, http.StatusBadRequest,
				"INVALID_INPUT",
				"必须提供 promptId，空字符串表示恢复默认提示词",
				nil,
			)
			return
		}

		selection, err := messageStore.SetSessionPrompt(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("id"),
			strings.TrimSpace(*request.PromptID),
		)
		if err != nil {
			writePromptError(c, err)
			return
		}

		c.JSON(http.StatusOK, selection)
	})
}

func writePromptError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrInvalidPrompt):
		writeError(
			c, http.StatusBadRequest,
			"INVALID_PROMPT",
			"名称必须为 1～100 个字符，内容必须为 1～20000 个字符",
			nil,
		)

	case errors.Is(err, store.ErrPromptNotFound):
		writeError(
			c, http.StatusNotFound,
			"PROMPT_NOT_FOUND", "提示词不存在", nil,
		)

	case errors.Is(err, sql.ErrNoRows):
		writeStoreError(c, err)

	default:
		writeError(
			c, http.StatusInternalServerError,
			"PROMPT_OPERATION_FAILED", "提示词操作失败", err,
		)
	}
}
