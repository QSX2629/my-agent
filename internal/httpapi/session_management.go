package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"my-agent/internal/store"
)

type renameSessionRequest struct {
	Title string `json:"title"`
}

// renameSessionHandler 生成修改会话标题的处理函数。
func renameSessionHandler(
	messageStore *store.Store,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request renameSessionRequest

		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(
				c,
				http.StatusBadRequest,
				"INVALID_JSON",
				"请求格式错误",
				nil,
			)
			return
		}

		session, err := messageStore.RenameOwnedSession(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("id"),
			request.Title,
		)
		if err != nil {
			writeSessionManagementError(c, err)
			return
		}

		c.JSON(http.StatusOK, session)
	}
}

// listSessionsHandler 生成查询会话列表的处理函数。
func listSessionsHandler(
	messageStore *store.Store,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, pageSize, paginated, err := readSessionPagination(c)
		if err != nil {
			writeSessionManagementError(c, err)
			return
		}

		if !paginated {
			sessions, err := messageStore.ListOwnedSessions(
				c.Request.Context(),
				c.GetString("userID"),
			)
			if err != nil {
				writeSessionManagementError(c, err)
				return
			}

			c.JSON(http.StatusOK, sessions)
			return
		}

		result, err := messageStore.ListOwnedSessionsPage(
			c.Request.Context(),
			c.GetString("userID"),
			page,
			pageSize,
		)
		if err != nil {
			writeSessionManagementError(c, err)
			return
		}

		c.JSON(http.StatusOK, result)
	}
}

// listMessagesHandler 生成查询历史消息的处理函数。
func listMessagesHandler(
	messageStore *store.Store,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, pageSize, paginated, err := readSessionPagination(c)
		if err != nil {
			writeSessionManagementError(c, err)
			return
		}

		if !paginated {
			messages, err := messageStore.LoadOwnedMessages(
				c.Request.Context(),
				c.GetString("userID"),
				c.Param("id"),
			)
			if err != nil {
				writeSessionManagementError(c, err)
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"sessionId": c.Param("id"),
				"messages":  messages,
			})
			return
		}

		result, err := messageStore.LoadOwnedMessagesPage(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("id"),
			page,
			pageSize,
		)
		if err != nil {
			writeSessionManagementError(c, err)
			return
		}

		c.JSON(http.StatusOK, result)
	}
}

// readSessionPagination 读取 URL 中的分页参数。
func readSessionPagination(
	c *gin.Context,
) (int64, int64, bool, error) {
	query := c.Request.URL.Query()

	paginated := query.Has("page") || query.Has("pageSize")

	page := int64(1)
	pageSize := int64(20)

	var err error

	if query.Has("page") {
		page, err = strconv.ParseInt(query.Get("page"), 10, 64)
		if err != nil {
			return 0, 0, true, store.ErrInvalidPagination
		}
	}

	if query.Has("pageSize") {
		pageSize, err = strconv.ParseInt(
			query.Get("pageSize"),
			10,
			64,
		)
		if err != nil {
			return 0, 0, true, store.ErrInvalidPagination
		}
	}

	return page, pageSize, paginated, nil
}

// writeSessionManagementError 把 Store 错误转换成 HTTP 错误。
func writeSessionManagementError(
	c *gin.Context,
	err error,
) {
	switch {
	case errors.Is(err, store.ErrInvalidTitle):
		writeError(
			c,
			http.StatusBadRequest,
			"INVALID_TITLE",
			"标题去掉首尾空白后必须为 1～100 个字符",
			nil,
		)

	case errors.Is(err, store.ErrInvalidPagination):
		writeError(
			c,
			http.StatusBadRequest,
			"INVALID_PAGINATION",
			"page 必须为正整数，pageSize 必须为 1～100，页码不能过大",
			nil,
		)

	default:
		writeStoreError(c, err)
	}
}
