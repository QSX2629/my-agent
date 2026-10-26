package skill

import (
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// 这些是本项目的教学限制，不是 Eino 的官方要求。
const (
	MaxFileBytes        = 6 * 1024
	MaxSkills           = 8
	MaxDescriptionBytes = 512
)

var ErrInvalid = errors.New("invalid skill document")

var namePattern = regexp.MustCompile(
	`^[a-z0-9]+(?:-[a-z0-9]+)*$`,
)

type Document struct {
	Name        string `json:"name"`
	Description string `json:"description"`

	// 查询列表时不通过 JSON 返回正文。
	Content string `json:"-"`

	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updatedAt"`
}

type frontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func ValidName(name string) bool {
	return len(name) >= 1 &&
		len(name) <= 64 &&
		namePattern.MatchString(name)
}

func Parse(filename string, data []byte) (Document, error) {
	if filename != "SKILL.md" ||
		len(data) == 0 ||
		len(data) > MaxFileBytes ||
		!utf8.Valid(data) {
		return Document{}, ErrInvalid
	}

	// 兼容 Windows 文件常见的 BOM 和 CRLF 换行。
	content := strings.TrimPrefix(string(data), "\uFEFF")
	content = strings.ReplaceAll(content, "\r\n", "\n")

	lines := strings.Split(content, "\n")

	if len(lines) < 4 || lines[0] != "---" {
		return Document{}, ErrInvalid
	}

	closing := -1

	for index := 1; index < len(lines); index++ {
		if lines[index] == "---" {
			closing = index
			break
		}
	}

	if closing < 0 {
		return Document{}, ErrInvalid
	}

	header := strings.Join(lines[1:closing], "\n")
	body := strings.TrimSpace(
		strings.Join(lines[closing+1:], "\n"),
	)

	if body == "" {
		return Document{}, ErrInvalid
	}

	var metadata frontMatter

	decoder := yaml.NewDecoder(strings.NewReader(header))

	// 本版只允许 name 和 description 两个元数据字段。
	decoder.KnownFields(true)

	if err := decoder.Decode(&metadata); err != nil {
		return Document{}, ErrInvalid
	}

	// 不允许一个头部包含多份 YAML 文档。
	var extra any

	if err := decoder.Decode(&extra); err != io.EOF {
		return Document{}, ErrInvalid
	}

	description := strings.Join(
		strings.Fields(metadata.Description),
		" ",
	)

	if !ValidName(metadata.Name) ||
		description == "" ||
		len(description) > MaxDescriptionBytes {
		return Document{}, ErrInvalid
	}

	return Document{
		Name:        metadata.Name,
		Description: description,
		Content:     content,
	}, nil
}
