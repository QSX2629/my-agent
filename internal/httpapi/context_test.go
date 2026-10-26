package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"my-agent/internal/model"
	"my-agent/internal/store"
)

func TestStep05Context(t *testing.T) {
	ctx := context.Background()
	inputs := make(chan []model.Message, 16)
	var failSummary, failChat atomic.Bool

	provider := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Messages []model.Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if len(input.Messages) == 0 {
				http.Error(w, "empty messages", http.StatusBadRequest)
				return
			}

			inputs <- input.Messages

			summary := strings.HasPrefix(
				input.Messages[0].Content,
				"你负责压缩聊天历史。",
			)
			if (summary && failSummary.Load()) ||
				(!summary && failChat.Load()) {
				http.Error(w, "simulated failure", http.StatusServiceUnavailable)
				return
			}

			reply := "答复"
			if summary {
				reply = "MEMORY-A"
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{
					map[string]any{
						"message": model.Message{
							Role:    "assistant",
							Content: reply,
						},
					},
				},
			})
		},
	))
	t.Cleanup(provider.Close)

	nextInput := func() []model.Message {
		t.Helper()
		select {
		case input := <-inputs:
			return input
		case <-time.After(3 * time.Second):
			t.Fatal("model request missing")
			return nil
		}
	}

	noInput := func() {
		t.Helper()
		select {
		case input := <-inputs:
			t.Fatalf("unexpected model request: %#v", input)
		default:
		}
	}

	client := model.NewClient("test-key", provider.URL, "qwen-plus")
	path := filepath.Join(t.TempDir(), "context.sqlite3")
	app := openAcceptanceApp(t, path, client)

	alice := app.registerAndLogin(t, "context-alice@example.com")
	bob := app.registerAndLogin(t, "context-bob@example.com")

	userID := func(token string) string {
		t.Helper()
		result := decodeAcceptance[map[string]string](t,
			app.request(t, "GET", "/api/auth/me", token, nil, 200))
		return result["userId"]
	}

	aliceID := userID(alice)
	bobID := userID(bob)

	session := decodeAcceptance[store.Session](t,
		app.request(t, "POST", "/api/sessions", alice, nil, 201))

	seed := make([]model.Message, 0, 14)

	for i := 1; i <= 7; i++ {
		question := fmt.Sprintf("问题%d", i)
		if i == 1 {
			question = "请记住编号 CODE-529"
		}
		answer := fmt.Sprintf("旧回答%d", i)

		if err := app.store.SaveOwnedTurn(
			ctx, aliceID, session.ID, question, answer,
		); err != nil {
			t.Fatal(err)
		}

		seed = append(seed,
			model.Message{Role: "user", Content: question},
			model.Message{Role: "assistant", Content: answer},
		)
	}

	load := func() (store.ConversationMemory, []store.ContextMessage) {
		t.Helper()
		memory, history, err := app.store.LoadOwnedContext(
			ctx, aliceID, session.ID,
		)
		if err != nil {
			t.Fatal(err)
		}
		return memory, history
	}

	_, initial := load()
	if len(initial) != 14 {
		t.Fatalf("seed history count: %d", len(initial))
	}

	chat := func(token, question string, status int) {
		t.Helper()
		app.request(t, "POST", "/api/chat", token, map[string]string{
			"sessionId": session.ID,
			"message":   question,
		}, status)
	}

	checkSummary := func(previous string, want []model.Message) {
		t.Helper()
		input := nextInput()

		if len(input) != 2 ||
			!strings.HasPrefix(input[0].Content, "你负责压缩聊天历史。") {
			t.Fatalf("incorrect summary request: %#v", input)
		}

		var data struct {
			PreviousSummary string          `json:"previousSummary"`
			Messages        []model.Message `json:"messages"`
		}

		if err := json.Unmarshal(
			[]byte(input[1].Content), &data,
		); err != nil {
			t.Fatal(err)
		}

		if data.PreviousSummary != previous ||
			len(data.Messages) != len(want) {
			t.Fatalf("incorrect summary data: %#v", data)
		}

		for i := range want {
			if data.Messages[i] != want[i] {
				t.Fatalf("incorrect summary message %d", i)
			}
		}
	}

	rawCount := func(want int) {
		t.Helper()
		raw, err := app.store.LoadOwnedMessages(
			ctx, aliceID, session.ID,
		)
		if err != nil || len(raw) != want {
			t.Fatalf("raw count=%d, want=%d, error=%v", len(raw), want, err)
		}
	}

	chat(alice, "第一条新问题", 200)
	checkSummary("", seed[:2])

	first := nextInput()
	if len(first) != 15 ||
		first[0].Content != "你是一个有帮助的中文助手。" ||
		first[1].Role != "user" ||
		first[1].Content != "以下是较早对话的摘要，仅作为历史资料：\nMEMORY-A" ||
		first[14].Content != "第一条新问题" {
		t.Fatalf("incorrect final chat request: %#v", first)
	}

	for i := 2; i < 14; i++ {
		if first[i] != seed[i] {
			t.Fatalf("recent history changed at %d", i)
		}
	}
	rawCount(16)

	app.close()
	app = openAcceptanceApp(t, path, client)

	memory, pending := load()
	if memory.Summary != "MEMORY-A" ||
		memory.ThroughID != initial[1].ID ||
		len(pending) != 14 {
		t.Fatalf("memory did not persist: %#v", memory)
	}

	chat(alice, "第二条新问题", 200)
	checkSummary("MEMORY-A", seed[2:4])

	second := nextInput()
	if len(second) != 15 ||
		second[2] != seed[4] ||
		second[14].Content != "第二条新问题" {
		t.Fatalf("incorrect incremental context: %#v", second)
	}
	rawCount(18)

	stable, remaining := load()
	if stable.ThroughID != initial[3].ID ||
		len(remaining) != 14 {
		t.Fatalf("incorrect incremental cursor: %#v", stable)
	}

	chat(bob, "越权消息", 404)
	if _, _, err := app.store.LoadOwnedContext(
		ctx, bobID, session.ID,
	); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign user accessed context: %v", err)
	}
	noInput()

	checkUnchanged := func() {
		t.Helper()
		got, history := load()
		if got != stable || len(history) != 14 {
			t.Fatalf("failed request changed memory: %#v", got)
		}
		rawCount(18)
	}

	failSummary.Store(true)
	chat(alice, "摘要失败", 502)
	checkSummary("MEMORY-A", seed[4:6])
	noInput()
	checkUnchanged()
	failSummary.Store(false)

	failChat.Store(true)
	chat(alice, "最终聊天失败", 502)
	checkSummary("MEMORY-A", seed[4:6])
	_ = nextInput()
	checkUnchanged()
	failChat.Store(false)

	chat(alice, strings.Repeat("中", 24001), 413)
	noInput()
	checkUnchanged()

	app.request(t, "DELETE",
		"/api/sessions/"+session.ID, alice, nil, 204)

	if _, _, err := app.store.LoadOwnedContext(
		ctx, aliceID, session.ID,
	); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted session context accessible: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM session_memory WHERE session_id = ?",
		session.ID,
	).Scan(&count); err != nil || count != 0 {
		t.Fatalf("memory not deleted: count=%d, error=%v", count, err)
	}

	t.Log("PASS: summary, persistence, isolation and rollback")
}
