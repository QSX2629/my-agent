package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"my-agent/internal/model"
	"my-agent/internal/store"
)

func TestStep04Prompts(t *testing.T) {
	inputs := make(chan []model.Message, 8)

	provider := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Messages []model.Message `json:"messages"`
			}

			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}

			inputs <- input.Messages

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w,
				`{"choices":[{"message":{"role":"assistant","content":"测试回答"}}]}`,
			)
		}),
	)
	t.Cleanup(provider.Close)

	client := model.NewClient("test-key", provider.URL, "qwen-plus")
	path := filepath.Join(t.TempDir(), "prompts.sqlite3")
	app := openAcceptanceApp(t, path, client)

	alice := app.registerAndLogin(t, "prompts-alice@example.com")
	bob := app.registerAndLogin(t, "prompts-bob@example.com")

	original := map[string]string{
		"name":    "Go 导师",
		"content": "你是一位 Go 语言导师。",
	}
	edited := map[string]string{
		"name":    "新导师",
		"content": "请用适合新手的方式解释 Go。",
	}

	app.request(t, "GET", "/api/prompts", "", nil, 401)
	app.request(t, "POST", "/api/prompts", alice,
		map[string]string{"name": " ", "content": "内容"}, 400)
	app.request(t, "POST", "/api/prompts", alice,
		map[string]string{"name": "名字", "content": " "}, 400)

	prompt := decodeAcceptance[store.Prompt](t,
		app.request(t, "POST", "/api/prompts", alice, original, 201))

	if prompt.ID == "" || prompt.Content != original["content"] {
		t.Fatalf("incorrect created prompt: %#v", prompt)
	}

	promptURL := "/api/prompts/" + prompt.ID

	got := decodeAcceptance[store.Prompt](t,
		app.request(t, "GET", promptURL, alice, nil, 200))
	if got.ID != prompt.ID || got.Content != original["content"] {
		t.Fatal("GET returned an incorrect prompt")
	}

	list := decodeAcceptance[[]store.Prompt](t,
		app.request(t, "GET", "/api/prompts", alice, nil, 200))
	if len(list) != 1 || list[0].ID != prompt.ID {
		t.Fatal("owner's prompt list is incorrect")
	}

	if body := app.request(
		t, "GET", "/api/prompts", bob, nil, 200,
	).Body.String(); body != "[]" {
		t.Fatalf("Bob should have no prompts, got %s", body)
	}

	for _, method := range []string{"GET", "PUT", "DELETE"} {
		app.request(t, method, promptURL, bob, edited, 404)
	}

	session := decodeAcceptance[store.Session](t,
		app.request(t, "POST", "/api/sessions", alice, nil, 201))
	bobSession := decodeAcceptance[store.Session](t,
		app.request(t, "POST", "/api/sessions", bob, nil, 201))

	bindingURL := "/api/sessions/" + session.ID + "/prompt"
	selection := map[string]string{"promptId": prompt.ID}

	for _, method := range []string{"GET", "PUT"} {
		app.request(t, method, bindingURL, bob, selection, 404)
	}

	app.request(t, "PUT",
		"/api/sessions/"+bobSession.ID+"/prompt",
		bob, selection, 404)

	app.request(t, "PUT", bindingURL, alice,
		map[string]string{}, 400)
	app.request(t, "PUT", bindingURL, alice,
		map[string]string{"promptId": "missing"}, 404)

	checkBinding := func(wantID string) {
		t.Helper()

		binding := decodeAcceptance[store.SessionPrompt](t,
			app.request(t, "GET", bindingURL, alice, nil, 200))

		if binding.SessionID != session.ID {
			t.Fatal("incorrect session ID")
		}

		if wantID == "" {
			if binding.Prompt != nil {
				t.Fatalf("expected default prompt, got %#v", binding.Prompt)
			}
		} else if binding.Prompt == nil || binding.Prompt.ID != wantID {
			t.Fatalf("incorrect binding: %#v", binding)
		}
	}

	checkBinding("")
	app.request(t, "PUT", bindingURL, alice, selection, 200)
	checkBinding(prompt.ID)

	// 关闭并重新打开数据库，检查选择是否保存。
	app.close()
	app = openAcceptanceApp(t, path, client)
	checkBinding(prompt.ID)

	turns := 0

	chat := func(wantSystem string) {
		t.Helper()

		app.request(t, "POST", "/api/chat", alice,
			map[string]string{
				"sessionId": session.ID,
				"message":   "请解释变量。",
			}, 200)

		var input []model.Message

		select {
		case input = <-inputs:
		case <-time.After(3 * time.Second):
			t.Fatal("model did not receive a request")
			return
		}

		if len(input) != turns*2+2 ||
			input[0].Role != "system" ||
			input[0].Content != wantSystem {
			t.Fatalf("incorrect model input: %#v", input)
		}

		turns++
	}

	chat(original["content"])

	updated := decodeAcceptance[store.Prompt](t,
		app.request(t, "PUT", promptURL, alice, edited, 200))
	if updated.Name != edited["name"] ||
		updated.Content != edited["content"] {
		t.Fatal("prompt update failed")
	}

	chat(edited["content"])

	app.request(t, "PUT", bindingURL, alice,
		map[string]string{"promptId": ""}, 200)
	checkBinding("")
	chat("你是一个有帮助的中文助手。")

	app.request(t, "PUT", bindingURL, alice, selection, 200)
	app.request(t, "DELETE", promptURL, alice, nil, 204)
	app.request(t, "GET", promptURL, alice, nil, 404)

	checkBinding("")
	chat("你是一个有帮助的中文助手。")

	history := decodeAcceptance[struct {
		Messages []model.Message `json:"messages"`
	}](t, app.request(t, "GET",
		"/api/sessions/"+session.ID+"/messages",
		alice, nil, 200))

	if len(history.Messages) != turns*2 {
		t.Fatal("prompt operations changed the message count")
	}

	t.Log("PASS: prompt CRUD, ownership, persistence and system messages")
}
