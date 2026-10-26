package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 验证接口地址、鉴权、模型名、消息顺序和回答解析。
func TestClientChat(t *testing.T) {
	wantMessages := []Message{
		{
			Role:    "system",
			Content: "你是一个中文助手。",
		},
		{
			Role:    "user",
			Content: "我叫小明。",
		},
		{
			Role:    "assistant",
			Content: "你好，小明。",
		},
		{
			Role:    "user",
			Content: "我叫什么？",
		},
	}

	server := httptest.NewServer(
		http.HandlerFunc(func(
			w http.ResponseWriter,
			r *http.Request,
		) {
			if r.Method != http.MethodPost {
				t.Errorf("unexpected method: %s", r.Method)
			}

			if r.URL.Path != "/v1/chat/completions" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			if r.Header.Get("Authorization") != "Bearer test-key" {
				t.Error("incorrect authorization header")
			}

			var body struct {
				Model    string    `json:"model"`
				Messages []Message `json:"messages"`
			}

			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}

			if body.Model != "test-model" {
				t.Errorf("unexpected model: %s", body.Model)
			}

			if !reflect.DeepEqual(body.Messages, wantMessages) {
				t.Errorf("unexpected messages: %#v", body.Messages)
			}

			w.Header().Set("Content-Type", "application/json")

			_, _ = io.WriteString(w, `{
				"id": "test-response",
				"object": "chat.completion",
				"model": "test-model",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "你叫小明。"
					},
					"finish_reason": "stop"
				}]
			}`)
		}),
	)
	defer server.Close()

	client := NewClient(
		"test-key",
		server.URL+"/v1",
		"test-model",
	)

	answer, err := client.Chat(
		context.Background(),
		wantMessages,
	)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if answer != "你叫小明。" {
		t.Fatalf("unexpected answer: %q", answer)
	}
}

// 验证模型异常、缺少候选回答以及空白回答。
func TestClientChatInvalidResponses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "upstream_error",
			status: http.StatusServiceUnavailable,
			body: `{
				"error": {
					"message": "TEST-UPSTREAM-DETAIL",
					"type": "server_error"
				}
			}`,
		},
		{
			name:   "missing_choices",
			status: http.StatusOK,
			body:   `{"choices":[]}`,
		},
		{
			name:   "empty_text",
			status: http.StatusOK,
			body: `{
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "   "
					},
					"finish_reason": "stop"
				}]
			}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(
					w http.ResponseWriter,
					r *http.Request,
				) {
					w.Header().Set(
						"Content-Type",
						"application/json",
					)

					w.WriteHeader(testCase.status)
					_, _ = io.WriteString(w, testCase.body)
				}),
			)
			defer server.Close()

			client := NewClient(
				"test-key",
				server.URL+"/v1",
				"test-model",
			)
			client.HTTPClient.Timeout = time.Second

			_, err := client.Chat(
				context.Background(),
				[]Message{
					{Role: "user", Content: "你好"},
				},
			)

			if err == nil {
				t.Fatal("expected an error")
			}

			if strings.Contains(
				err.Error(),
				"TEST-UPSTREAM-DETAIL",
			) {
				t.Fatal("error exposed upstream response details")
			}
		})
	}
}

// 验证取消和超时仍能被上层正确识别。
func TestClientChatContext(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(func(
			w http.ResponseWriter,
			r *http.Request,
		) {
			_, _ = io.Copy(io.Discard, r.Body)

			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}),
	)
	defer server.Close()

	messages := []Message{
		{Role: "user", Content: "你好"},
	}

	t.Run("canceled", func(t *testing.T) {
		client := NewClient(
			"test-key",
			server.URL+"/v1",
			"test-model",
		)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := client.Chat(ctx, messages)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled error, got: %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		client := NewClient(
			"test-key",
			server.URL+"/v1",
			"test-model",
		)
		client.HTTPClient.Timeout = 100 * time.Millisecond

		_, err := client.Chat(
			context.Background(),
			messages,
		)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline error, got: %v", err)
		}
	})
}
