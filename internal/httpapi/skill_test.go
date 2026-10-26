package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"my-agent/internal/model"
	"my-agent/internal/skill"
	"my-agent/internal/store"
)

const step09Document = `---
name: time-report
description: 生成北京时间简报时使用。
---

BODY_SECRET
请调用 get_current_time 获取真实时间，再生成简报。
`

func step09Upload(
	t *testing.T,
	app *acceptanceApp,
	token string,
	document string,
	wantStatus int,
) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := part.Write([]byte(document)); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		"POST",
		"/api/skills",
		&body,
	)
	request.Header.Set(
		"Content-Type",
		writer.FormDataContentType(),
	)

	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response := httptest.NewRecorder()
	app.handler.ServeHTTP(response, request)

	if response.Code != wantStatus {
		t.Fatalf(
			"upload: got %d, want %d; body=%s",
			response.Code,
			wantStatus,
			response.Body.String(),
		)
	}

	return response
}

func TestStep09Skills(t *testing.T) {
	var requests atomic.Int32

	provider := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Messages []*schema.Message `json:"messages"`

				Tools []struct {
					Function struct {
						Name        string `json:"name"`
						Description string `json:"description"`
					} `json:"function"`
				} `json:"tools"`
			}

			if err := json.NewDecoder(r.Body).Decode(&input); err != nil ||
				len(input.Messages) == 0 {
				t.Error("invalid model input")
				http.Error(w, "invalid input", 400)
				return
			}

			hasTime := false
			hasLoad := false
			catalog := ""

			for _, definition := range input.Tools {
				switch definition.Function.Name {
				case "get_current_time":
					hasTime = true

				case "load_skill":
					hasLoad = true
					catalog = definition.Function.Description
				}
			}

			if !hasTime {
				t.Error("time tool is missing")
			}

			last := input.Messages[len(input.Messages)-1]
			var output string

			switch requests.Add(1) {
			case 1:
				if !hasLoad ||
					!strings.Contains(catalog, "time-report") ||
					strings.Contains(catalog, "BODY_SECRET") ||
					strings.Contains(catalog, "bob-private") {
					t.Error("incorrect skill catalog")
				}

				for _, message := range input.Messages {
					if strings.Contains(message.Content, "BODY_SECRET") {
						t.Error("skill body was loaded before tool call")
					}
				}

				output = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"skill-1","type":"function","function":{"name":"load_skill","arguments":"{\"name\":\"time-report\"}"}}]},"finish_reason":"tool_calls"}]}`

			case 2:
				if last.Role != schema.Tool ||
					last.ToolCallID != "skill-1" ||
					last.Content != step09Document {
					t.Error("incorrect skill tool result")
				}

				output = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"time-1","type":"function","function":{"name":"get_current_time","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`

			case 3:
				var result struct {
					Timestamp string `json:"timestamp"`
					Timezone  string `json:"timezone"`
				}

				err := json.Unmarshal(
					[]byte(last.Content),
					&result,
				)

				if err != nil ||
					last.Role != schema.Tool ||
					last.ToolCallID != "time-1" ||
					result.Timezone != "Asia/Shanghai" {
					t.Error("incorrect time tool result")
				}

				if _, err := time.Parse(
					time.RFC3339,
					result.Timestamp,
				); err != nil {
					t.Error("invalid timestamp")
				}

				output = `{"choices":[{"message":{"role":"assistant","content":"已按指南生成北京时间简报。"},"finish_reason":"stop"}]}`

			case 4:
				if hasLoad {
					t.Error("disabled skill is still available")
				}

				output = `{"choices":[{"message":{"role":"assistant","content":"当前没有启用的指南。"},"finish_reason":"stop"}]}`

			case 5:
				if !hasLoad {
					t.Error("enabled skill loader is missing")
				}

				// 故意要求加载另一个用户的指南。
				output = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"forbidden-1","type":"function","function":{"name":"load_skill","arguments":"{\"name\":\"bob-private\"}"}}]},"finish_reason":"tool_calls"}]}`

			default:
				t.Error("unexpected extra model request")
				http.Error(w, "unexpected request", 500)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, output)
		},
	))
	defer provider.Close()

	client := model.NewClient(
		"test-key",
		provider.URL,
		"qwen-plus",
	)

	databasePath := filepath.Join(t.TempDir(), "skills.sqlite3")
	app := openAcceptanceApp(t, databasePath, client)

	alice := app.registerAndLogin(t, "skill-alice@example.com")
	bob := app.registerAndLogin(t, "skill-bob@example.com")

	step09Upload(t, app, "", step09Document, 401)

	// 上传 BOM + Windows 换行版本。
	windowsDocument := "\uFEFF" +
		strings.ReplaceAll(step09Document, "\n", "\r\n")

	response := step09Upload(
		t, app, alice, windowsDocument, 200,
	)

	if bytes.Contains(response.Body.Bytes(), []byte("BODY_SECRET")) {
		t.Fatal("upload response exposed skill content")
	}

	bobList := decodeAcceptance[[]skill.Document](
		t,
		app.request(t, "GET", "/api/skills", bob, nil, 200),
	)

	if len(bobList) != 0 {
		t.Fatal("Bob can see Alice's skills")
	}

	app.request(
		t, "PATCH", "/api/skills/time-report", bob,
		map[string]bool{"enabled": false}, 404,
	)
	app.request(
		t, "DELETE", "/api/skills/time-report", bob, nil, 404,
	)

	bobDocument := strings.ReplaceAll(
		step09Document,
		"time-report",
		"bob-private",
	)

	step09Upload(t, app, bob, bobDocument, 200)

	session := decodeAcceptance[store.Session](
		t,
		app.request(t, "POST", "/api/sessions", alice, nil, 201),
	)

	payload := map[string]string{
		"sessionId": session.ID,
		"message":   "请按指南生成北京时间简报。",
	}

	app.request(t, "POST", "/api/chat", alice, payload, 200)

	if requests.Load() != 3 {
		t.Fatal("expected load_skill -> time tool -> final answer")
	}

	history := decodeAcceptance[struct {
		Messages []model.Message `json:"messages"`
	}](
		t,
		app.request(
			t, "GET",
			"/api/sessions/"+session.ID+"/messages",
			alice, nil, 200,
		),
	)

	if len(history.Messages) != 2 ||
		history.Messages[0].Role != "user" ||
		history.Messages[1].Role != "assistant" {
		t.Fatal("tool messages should not be saved as chat history")
	}

	app.request(
		t, "PATCH", "/api/skills/time-report", alice,
		map[string]bool{"enabled": false}, 204,
	)

	updatedDocument := strings.ReplaceAll(
		step09Document,
		"生成北京时间简报时使用。",
		"更新后的指南描述。",
	)

	updated := decodeAcceptance[skill.Document](
		t,
		step09Upload(t, app, alice, updatedDocument, 200),
	)

	if updated.Enabled {
		t.Fatal("updating a disabled skill enabled it")
	}

	// 重开数据库，验证保存结果。
	app.close()
	app = openAcceptanceApp(t, databasePath, client)

	list := decodeAcceptance[[]skill.Document](
		t,
		app.request(t, "GET", "/api/skills", alice, nil, 200),
	)

	if len(list) != 1 ||
		list[0].Enabled ||
		list[0].Description != "更新后的指南描述。" {
		t.Fatal("skill state was not persisted")
	}

	// 停用后，新一轮不应提供 load_skill。
	app.request(t, "POST", "/api/chat", alice, payload, 200)

	app.request(
		t, "PATCH", "/api/skills/time-report", alice,
		map[string]bool{"enabled": true}, 204,
	)

	// 模型要求加载 Bob 的 Skill，应被拒绝。
	app.request(t, "POST", "/api/chat", alice, payload, 502)

	history = decodeAcceptance[struct {
		Messages []model.Message `json:"messages"`
	}](
		t,
		app.request(
			t, "GET",
			"/api/sessions/"+session.ID+"/messages",
			alice, nil, 200,
		),
	)

	if len(history.Messages) != 4 || requests.Load() != 5 {
		t.Fatal("failed turn changed history or reached the model again")
	}

	step09Upload(t, app, alice, "invalid document", 400)

	step09Upload(
		t, app, alice,
		strings.Repeat("x", skill.MaxFileBytes+1),
		413,
	)

	app.request(
		t, "DELETE", "/api/skills/time-report", alice, nil, 204,
	)
}
