package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"my-agent/internal/agent"
	"my-agent/internal/auth"
	"my-agent/internal/httpapi"
	"my-agent/internal/model"
	"my-agent/internal/store"
)

type acceptanceApp struct {
	store   *store.Store
	handler http.Handler
	closed  bool
}

func openAcceptanceApp(t *testing.T, path string, client *model.Client) *acceptanceApp {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	app := &acceptanceApp{store: db}
	t.Cleanup(app.close)
	for range 2 {
		if err := db.MigrateSessionOwners(); err != nil {
			t.Fatal(err)
		}
	}
	app.handler = httpapi.NewRouter(agent.NewService(client, db), db, auth.NewService(db))
	return app
}

func (a *acceptanceApp) close() {
	if !a.closed {
		a.closed = true
		_ = a.store.Close()
	}
}

func (a *acceptanceApp) request(t *testing.T, method, path, token string, payload any, want int) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	a.handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s: got HTTP %d, want %d; body=%s", method, path, response.Code, want, response.Body.String())
	}
	return response
}

func decodeAcceptance[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (a *acceptanceApp) registerAndLogin(t *testing.T, email string) string {
	t.Helper()
	credentials := map[string]string{"email": email, "password": "TestPassword123!"}
	a.request(t, "POST", "/api/auth/register", "", credentials, 201)
	response := a.request(t, "POST", "/api/auth/login", "", credentials, 200)
	login := decodeAcceptance[struct{ Token string }](t, response)
	if login.Token == "" {
		t.Fatal("login returned an empty token")
	}
	return login.Token
}

func TestStep01Acceptance(t *testing.T) {
	var requestMu sync.Mutex
	var modelRequests [][]model.Message
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Error("model request has incorrect path or authentication")
			http.Error(w, "invalid model request", 400)
			return
		}
		var input struct {
			Model    string          `json:"model"`
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "qwen-plus" || len(input.Messages) == 0 {
			t.Error("invalid model request body")
			http.Error(w, "invalid model request body", 400)
			return
		}
		requestMu.Lock()
		modelRequests = append(modelRequests, input.Messages)
		requestMu.Unlock()
		if input.Messages[len(input.Messages)-1].Content == "simulate failure" {
			http.Error(w, "simulated upstream failure", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"你叫小明。"}}]}`)
	}))
	defer provider.Close()
	client := model.NewClient("test-api-key", provider.URL, "qwen-plus")
	databasePath := filepath.Join(t.TempDir(), "chat.sqlite3")
	app := openAcceptanceApp(t, databasePath, client)
	app.request(t, "GET", "/health", "", nil, 200)

	for _, path := range []string{"/api/auth/me", "/api/sessions", "/api/sessions/missing/messages"} {
		app.request(t, "GET", path, "", nil, 401)
		app.request(t, "GET", path, "unknown-token", nil, 401)
	}
	for _, header := range []string{"Basic abc", "Bearer", "Bearer abc extra"} {
		request := httptest.NewRequest("GET", "/api/auth/me", nil)
		request.Header.Set("Authorization", header)
		response := httptest.NewRecorder()
		app.handler.ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatalf("malformed authorization header accepted: HTTP %d", response.Code)
		}
	}
	t.Log("PASS: health, missing/unknown/malformed tokens")

	aliceToken := app.registerAndLogin(t, "alice@example.com")
	bobToken := app.registerAndLogin(t, "bob@example.com")
	app.request(t, "POST", "/api/auth/login", "", map[string]string{"email": "alice@example.com", "password": "wrong"}, 401)
	app.request(t, "POST", "/api/auth/register", "", map[string]string{"email": "ALICE@example.com", "password": "TestPassword123!"}, 400)
	alice := decodeAcceptance[struct{ UserID string }](t, app.request(t, "GET", "/api/auth/me", aliceToken, nil, 200))
	if alice.UserID == "" {
		t.Fatal("authenticated user ID is empty")
	}
	if got := app.request(t, "GET", "/api/sessions", bobToken, nil, 200).Body.String(); got != "[]" {
		t.Fatalf("new user's session list should be [], got %s", got)
	}
	session := decodeAcceptance[store.Session](t, app.request(t, "POST", "/api/sessions", aliceToken, nil, 201))
	if session.ID == "" {
		t.Fatal("created session has no ID")
	}
	empty := decodeAcceptance[struct{ Messages []model.Message }](t, app.request(t, "GET", "/api/sessions/"+session.ID+"/messages", aliceToken, nil, 200))
	if empty.Messages == nil || len(empty.Messages) != 0 {
		t.Fatal("empty session should contain an empty JSON array")
	}
	chat := func(message string) map[string]string {
		return map[string]string{"sessionId": session.ID, "message": message}
	}
	app.request(t, "POST", "/api/chat", aliceToken, chat("我叫小明"), 200)

	app.request(t, "GET", "/api/sessions/"+session.ID+"/messages", bobToken, nil, 404)
	app.request(t, "POST", "/api/chat", bobToken, chat("我叫什么？"), 404)
	app.request(t, "DELETE", "/api/sessions/"+session.ID, bobToken, nil, 404)
	app.request(t, "GET", "/api/sessions/missing/messages", aliceToken, nil, 404)
	if got := app.request(t, "GET", "/api/sessions", bobToken, nil, 200).Body.String(); got != "[]" {
		t.Fatal("another user's session appeared in Bob's list")
	}
	requestMu.Lock()
	count := len(modelRequests)
	requestMu.Unlock()
	if count != 1 {
		t.Fatal("unauthorized chat reached the model")
	}
	t.Log("PASS: registration/login, empty sessions, cross-user list/read/chat/delete isolation")

	app.close()
	app = openAcceptanceApp(t, databasePath, client)
	app.request(t, "GET", "/api/auth/me", aliceToken, nil, 200)
	sessions := decodeAcceptance[[]store.Session](t, app.request(t, "GET", "/api/sessions", aliceToken, nil, 200))
	if len(sessions) != 1 || sessions[0].ID != session.ID {
		t.Fatal("session was not preserved after reopening the database")
	}
	app.request(t, "POST", "/api/chat", aliceToken, chat("我叫什么？"), 200)
	requestMu.Lock()
	secondInput := modelRequests[1]
	requestMu.Unlock()
	if len(secondInput) != 4 || secondInput[0].Role != "system" || secondInput[1].Content != "我叫小明" || secondInput[2].Role != "assistant" || secondInput[3].Content != "我叫什么？" {
		t.Fatalf("second model request did not include the complete ordered history: %#v", secondInput)
	}
	history := decodeAcceptance[struct{ Messages []model.Message }](t, app.request(t, "GET", "/api/sessions/"+session.ID+"/messages", aliceToken, nil, 200))
	if len(history.Messages) != 4 {
		t.Fatalf("want 4 persisted messages, got %d", len(history.Messages))
	}
	app.request(t, "POST", "/api/chat", aliceToken, chat("simulate failure"), 502)
	afterFailure := decodeAcceptance[struct{ Messages []model.Message }](t, app.request(t, "GET", "/api/sessions/"+session.ID+"/messages", aliceToken, nil, 200))
	if len(afterFailure.Messages) != len(history.Messages) {
		t.Fatal("failed model request changed the saved history")
	}
	t.Log("PASS: restart persistence, token persistence, ordered model history, failed model call saves no messages")

	expiredToken := "expired-test-token"
	sum := sha256.Sum256([]byte(expiredToken))
	if err := app.store.SaveToken(hex.EncodeToString(sum[:]), alice.UserID, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	app.request(t, "GET", "/api/auth/me", expiredToken, nil, 401)
	app.request(t, "DELETE", "/api/sessions/"+session.ID, aliceToken, nil, 204)
	app.request(t, "GET", "/api/sessions/"+session.ID+"/messages", aliceToken, nil, 404)
	app.request(t, "POST", "/api/chat", aliceToken, chat("deleted"), 404)
	inspection, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var remaining int
	if err := inspection.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", session.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("deleted session left messages: count=%d err=%v", remaining, err)
	}
	app.request(t, "POST", "/api/auth/logout", aliceToken, nil, 204)
	app.request(t, "GET", "/api/auth/me", aliceToken, nil, 401)
	app.request(t, "GET", "/api/sessions", aliceToken, nil, 401)
	t.Log("PASS: expired tokens, deletion removes messages, logout revokes token")
}

// Opt-in only: this makes two real, potentially billable model requests.
func TestStep01RealModel(t *testing.T) {
	if os.Getenv("STEP01_REAL_MODEL") != "1" {
		t.Skip("set STEP01_REAL_MODEL=1 to verify the real Qwen service")
	}
	apiKey := os.Getenv("DASHSCOPE_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		t.Fatal("DASHSCOPE_API_KEY is not available")
	}
	client := model.NewClient(apiKey, "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen-plus")
	databasePath := filepath.Join(t.TempDir(), "real-chat.sqlite3")
	app := openAcceptanceApp(t, databasePath, client)
	token := app.registerAndLogin(t, "real-model-test@example.com")
	session := decodeAcceptance[store.Session](t, app.request(t, "POST", "/api/sessions", token, nil, 201))
	code := "CHECK-" + time.Now().UTC().Format("20060102150405.000000000")
	app.request(t, "POST", "/api/chat", token, map[string]string{"sessionId": session.ID, "message": "请记住我的验收代号是 " + code + "。请简短确认。"}, 200)
	app.close()
	app = openAcceptanceApp(t, databasePath, client)
	response := app.request(t, "POST", "/api/chat", token, map[string]string{"sessionId": session.ID, "message": "我的验收代号是什么？请只返回之前告诉你的完整代号。"}, 200)
	reply := decodeAcceptance[struct{ Reply string }](t, response)
	if !strings.Contains(reply.Reply, code) {
		t.Fatal("the real model did not recall the saved verification code after restart")
	}
	messages, err := app.store.LoadOwnedMessages(context.Background(), mustUserID(t, app, token), session.ID)
	if err != nil || len(messages) != 4 {
		t.Fatalf("real model history persistence failed: count=%d err=%v", len(messages), err)
	}
	t.Log("PASS: two real Qwen requests, conversation remembered after database reopen")
}

func mustUserID(t *testing.T, app *acceptanceApp, token string) string {
	t.Helper()
	return decodeAcceptance[struct{ UserID string }](t, app.request(t, "GET", "/api/auth/me", token, nil, 200)).UserID
}
