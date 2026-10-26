package httpapi

import (
	"database/sql"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"my-agent/internal/skill"
	"my-agent/internal/store"
)

// 包括文件内容和 multipart 请求中的其他信息。
const maxSkillUploadBytes = 16 * 1024

func registerSkillRoutes(
	protected *gin.RouterGroup,
	messageStore *store.Store,
) {
	protected.POST("/skills", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(
			c.Writer,
			c.Request.Body,
			maxSkillUploadBytes,
		)

		defer func() {
			if c.Request.MultipartForm != nil {
				_ = c.Request.MultipartForm.RemoveAll()
			}
		}()

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			var tooLarge *http.MaxBytesError

			if errors.As(err, &tooLarge) {
				writeSkillTooLarge(c)
				return
			}

			writeError(
				c,
				http.StatusBadRequest,
				"INVALID_UPLOAD",
				"请使用 file 字段上传 SKILL.md",
				nil,
			)
			return
		}
		defer file.Close()

		data, err := io.ReadAll(
			io.LimitReader(file, skill.MaxFileBytes+1),
		)
		if err != nil {
			writeError(
				c,
				http.StatusBadRequest,
				"INVALID_UPLOAD",
				"读取上传文件失败",
				nil,
			)
			return
		}

		if len(data) > skill.MaxFileBytes {
			writeSkillTooLarge(c)
			return
		}

		document, err := messageStore.SaveOwnedSkill(
			c.Request.Context(),
			c.GetString("userID"),
			header.Filename,
			data,
		)
		if err != nil {
			writeSkillError(c, err)
			return
		}

		c.JSON(http.StatusOK, document)
	})

	protected.GET("/skills", func(c *gin.Context) {
		documents, err := messageStore.ListOwnedSkills(
			c.Request.Context(),
			c.GetString("userID"),
			false,
		)
		if err != nil {
			writeSkillError(c, err)
			return
		}

		c.JSON(http.StatusOK, documents)
	})

	protected.PATCH("/skills/:name", func(c *gin.Context) {
		var request struct {
			Enabled *bool `json:"enabled"`
		}

		if err := c.ShouldBindJSON(&request); err != nil ||
			request.Enabled == nil {
			writeError(
				c,
				http.StatusBadRequest,
				"INVALID_INPUT",
				"必须提供 enabled，值为 true 或 false",
				nil,
			)
			return
		}

		err := messageStore.SetOwnedSkillEnabled(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("name"),
			*request.Enabled,
		)
		if err != nil {
			writeSkillError(c, err)
			return
		}

		c.Status(http.StatusNoContent)
	})

	protected.DELETE("/skills/:name", func(c *gin.Context) {
		err := messageStore.DeleteOwnedSkill(
			c.Request.Context(),
			c.GetString("userID"),
			c.Param("name"),
		)
		if err != nil {
			writeSkillError(c, err)
			return
		}

		c.Status(http.StatusNoContent)
	})
}

func writeSkillTooLarge(c *gin.Context) {
	writeError(
		c,
		http.StatusRequestEntityTooLarge,
		"SKILL_TOO_LARGE",
		"文件不能超过 6 KiB，完整上传请求不能超过 16 KiB",
		nil,
	)
}

func writeSkillError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, skill.ErrInvalid):
		writeError(
			c,
			http.StatusBadRequest,
			"INVALID_SKILL",
			"SKILL.md 格式、名称或描述不符合要求",
			nil,
		)

	case errors.Is(err, store.ErrSkillLimit):
		writeError(
			c,
			http.StatusConflict,
			"SKILL_LIMIT",
			"每个用户最多保存 8 个 Skill，请先删除部分记录",
			nil,
		)

	case errors.Is(err, sql.ErrNoRows):
		writeError(
			c,
			http.StatusNotFound,
			"SKILL_NOT_FOUND",
			"Skill 不存在",
			nil,
		)

	default:
		writeError(
			c,
			http.StatusInternalServerError,
			"SKILL_OPERATION_FAILED",
			"Skill 操作失败",
			err,
		)
	}
}
