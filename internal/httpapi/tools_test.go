package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"my-agent/internal/model"
	"my-agent/internal/store"
)

func TestStep07Tools(t *testing.T) {
	var requests atomic.Int32
	var mode atomic.Int32

	provider := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			number := requests.Add(1)

			var input struct {
				Messages []*schema.Message `json:"messages"`
				Tools    []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}

			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid request", 400)
				return
			}

			if len(input.Messages) == 0 ||
				len(input.Tools) != 1 ||
				input.Tools[0].Function.Name != "get_current_time" {
				t.Error("model request is missing messages or tools")
				http.Error(w, "invalid tools", 400)
				return
			}

			write := func(message map[string]any, finish string) {
				w.Header().Set("Content-Type", "application/json")

				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{
						map[string]any{
							"index":         0,
							"message":       message,
							"finish_reason": finish,
						},
					},
				})
			}

			last := input.Messages[len(input.Messages)-1]
			currentMode := mode.Load()

			if last.Role == schema.Tool {
				previous := input.Messages[len(input.Messages)-2]

				if previous.Role != schema.Assistant ||
					len(previous.ToolCalls) != 1 ||
					last.ToolCallID != previous.ToolCalls[0].ID {
					t.Error("tool result is not linked to its call")
					http.Error(w, "invalid tool linkage", 400)
					return
				}

				var result struct {
					Timestamp string `json:"timestamp"`
					Timezone  string `json:"timezone"`
				}

				if err := json.Unmarshal(
					[]byte(last.Content),
					&result,
				); err != nil {
					t.Error("tool result is not valid JSON")
					http.Error(w, "invalid result", 400)
					return
				}

				parsed, err := time.Parse(
					time.RFC3339,
					result.Timestamp,
				)
				if err != nil {
					t.Error("tool returned an invalid timestamp")
					http.Error(w, "invalid time", 400)
					return
				}

				_, offset := parsed.Zone()

				if result.Timezone != "Asia/Shanghai" ||
					offset != 8*60*60 {
					t.Error("tool returned an incorrect time zone")
					http.Error(w, "invalid zone", 400)
					return
				}

				if currentMode == 1 {
					http.Error(w, "simulated final failure", 503)
					return
				}

				// 模式 4 和 6 故意继续要求调用工具。
				if currentMode != 4 && currentMode != 6 {
					write(map[string]any{
						"role":    "assistant",
						"content": "测试：已获取当前北京时间。",
					}, "stop")
					return
				}
			}

			name := "get_current_time"
			arguments := "{}"
			id := fmt.Sprintf("call-%d", number)

			switch currentMode {
			case 2:
				arguments = `{"unexpected":true}`
			case 3:
				name = "unknown_tool"
			case 5:
				id = ""
			case 6:
				id = "repeated-call"
			}

			call := map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": arguments,
				},
			}

			write(map[string]any{
				"role":       "assistant",
				"tool_calls": []any{call},
			}, "tool_calls")
		},
	))
	defer provider.Close()

	client := model.NewClient(
		"test-key",
		provider.URL,
		"qwen-plus",
	)

	app := openAcceptanceApp(
		t,
		filepath.Join(t.TempDir(), "tools.sqlite3"),
		client,
	)

	alice := app.registerAndLogin(t, "tools-alice@example.com")
	bob := app.registerAndLogin(t, "tools-bob@example.com")

	session := decodeAcceptance[store.Session](t,
		app.request(t, "POST", "/api/sessions", alice, nil, 201),
	)

	payload := map[string]string{
		"sessionId": session.ID,
		"message":   "请调用工具查询当前北京时间。",
	}

	// 越权请求不能进入模型和工具执行流程。
	app.request(t, "POST", "/api/chat", bob, payload, 404)

	if requests.Load() != 0 {
		t.Fatal("unauthorized request reached the model")
	}

	// 正常闭环：模型要求工具 → 工具结果 → 模型最终回答。
	app.request(t, "POST", "/api/chat", alice, payload, 200)

	if requests.Load() != 2 {
		t.Fatalf("expected 2 model requests, got %d", requests.Load())
	}

	checkHistory := func() {
		t.Helper()

		history := decodeAcceptance[struct {
			Messages []model.Message `json:"messages"`
		}](t, app.request(
			t,
			"GET",
			"/api/sessions/"+session.ID+"/messages",
			alice,
			nil,
			200,
		))

		if len(history.Messages) != 2 ||
			history.Messages[0].Role != "user" ||
			history.Messages[1].Role != "assistant" ||
			history.Messages[1].Content != "测试：已获取当前北京时间。" {
			t.Fatalf("unexpected saved history: %#v", history.Messages)
		}
	}

	checkHistory()

	failures := []struct {
		name string
		mode int32
	}{
		{"final_model_failure", 1},
		{"invalid_arguments", 2},
		{"unknown_tool", 3},
		{"tool_limit", 4},
		{"empty_call_id", 5},
		{"repeated_call_id", 6},
	}

	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			mode.Store(failure.mode)

			app.request(
				t,
				"POST",
				"/api/chat",
				alice,
				payload,
				502,
			)

			// 失败后仍只有之前成功保存的两条消息。
			checkHistory()
		})
	}
}
