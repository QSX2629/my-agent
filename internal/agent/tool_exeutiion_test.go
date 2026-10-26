package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type step08Tool struct {
	run func(context.Context, string) (string, error)
}

func (f *step08Tool) Info(
	context.Context,
) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "test_tool",
		Desc: "用于本地测试的工具",
	}, nil
}

func (f *step08Tool) InvokableRun(
	ctx context.Context,
	arguments string,
	_ ...einotool.Option,
) (string, error) {
	return f.run(ctx, arguments)
}

func newStep08Executor(
	t *testing.T,
	run func(context.Context, string) (string, error),
) (*toolExecutor, *bytes.Buffer) {
	t.Helper()

	logs := &bytes.Buffer{}

	executor, err := newToolExecutor(
		context.Background(),
		toolScope{
			runID:     "run-test",
			userID:    "user-test",
			sessionID: "session-test",
		},
		[]einotool.InvokableTool{
			&step08Tool{run: run},
		},
		slog.New(slog.NewJSONHandler(logs, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}

	return executor, logs
}

func step08Call(id, arguments string) schema.ToolCall {
	call := schema.ToolCall{
		ID:   id,
		Type: "function",
	}

	call.Function.Name = "test_tool"
	call.Function.Arguments = arguments

	return call
}

func TestStep08Audit(t *testing.T) {
	executor, logs := newStep08Executor(
		t,
		func(context.Context, string) (string, error) {
			return "PRIVATE_RESULT", nil
		},
	)

	results, err := executor.Execute(
		context.Background(),
		[]schema.ToolCall{
			step08Call(
				"call-1",
				`{"note":"PRIVATE_ARGUMENT"}`,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 ||
		results[0].Role != schema.Tool ||
		results[0].ToolCallID != "call-1" ||
		results[0].Content != "PRIVATE_RESULT" {
		t.Fatalf("unexpected tool results: %#v", results)
	}

	if strings.Contains(logs.String(), "PRIVATE_ARGUMENT") ||
		strings.Contains(logs.String(), "PRIVATE_RESULT") {
		t.Fatal("audit log contains private content")
	}

	lines := strings.Split(
		strings.TrimSpace(logs.String()),
		"\n",
	)

	if len(lines) != 2 {
		t.Fatalf("expected 2 audit events, got %d", len(lines))
	}

	for index, wantStatus := range []string{
		"started",
		"completed",
	} {
		var event struct {
			RunID     string `json:"runId"`
			UserID    string `json:"userId"`
			SessionID string `json:"sessionId"`
			Status    string `json:"status"`
		}

		if err := json.Unmarshal(
			[]byte(lines[index]),
			&event,
		); err != nil {
			t.Fatal(err)
		}

		if event.Status != wantStatus ||
			event.RunID != "run-test" ||
			event.UserID != "user-test" ||
			event.SessionID != "session-test" {
			t.Fatalf("unexpected audit event: %#v", event)
		}
	}
}

func TestStep08ToolTimeout(t *testing.T) {
	executor, _ := newStep08Executor(
		t,
		func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	)

	executor.limits.Timeout = 20 * time.Millisecond

	_, err := executor.Execute(
		context.Background(),
		[]schema.ToolCall{
			step08Call("call-1", "{}"),
		},
	)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
}

func TestStep08ErrorRedaction(t *testing.T) {
	source := errors.New("PRIVATE_ERROR")

	executor, logs := newStep08Executor(
		t,
		func(context.Context, string) (string, error) {
			return "", source
		},
	)

	_, err := executor.Execute(
		context.Background(),
		[]schema.ToolCall{
			step08Call("call-1", "{}"),
		},
	)

	if !errors.Is(err, source) {
		t.Fatal("original error was not preserved")
	}

	if strings.Contains(err.Error(), "PRIVATE_ERROR") ||
		strings.Contains(logs.String(), "PRIVATE_ERROR") {
		t.Fatal("error details leaked into public text or logs")
	}
}

func TestStep08Limits(t *testing.T) {
	cases := []struct {
		name      string
		calls     []schema.ToolCall
		maxArgs   int
		maxResult int
		maxCalls  int
		wantRuns  int
		wantCode  string
	}{
		{
			name: "argument_size",
			calls: []schema.ToolCall{
				step08Call("call-1", `{"x":1}`),
			},
			maxArgs:  2,
			wantCode: "TOOL_ARGUMENTS_TOO_LARGE",
		},
		{
			name: "result_size",
			calls: []schema.ToolCall{
				step08Call("call-1", "{}"),
			},
			maxResult: 2,
			wantRuns:  1,
			wantCode:  "TOOL_RESULT_TOO_LARGE",
		},
		{
			name: "call_count",
			calls: []schema.ToolCall{
				step08Call("call-1", "{}"),
				step08Call("call-2", "{}"),
			},
			maxCalls: 1,
			wantCode: "TOOL_CALL_LIMIT",
		},
		{
			name: "validate_whole_batch",
			calls: []schema.ToolCall{
				step08Call("call-1", "{}"),
				step08Call("call-2", "[]"),
			},
			wantCode: "INVALID_TOOL_ARGUMENTS",
		},
		{
			name: "duplicate_ids",
			calls: []schema.ToolCall{
				step08Call("call-1", "{}"),
				step08Call("call-1", "{}"),
			},
			wantCode: "DUPLICATE_TOOL_CALL_ID",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runs := 0

			executor, _ := newStep08Executor(
				t,
				func(context.Context, string) (string, error) {
					runs++
					return "RESULT", nil
				},
			)

			if testCase.maxArgs > 0 {
				executor.limits.MaxArgumentBytes = testCase.maxArgs
			}

			if testCase.maxResult > 0 {
				executor.limits.MaxResultBytes = testCase.maxResult
			}

			if testCase.maxCalls > 0 {
				executor.limits.MaxCalls = testCase.maxCalls
			}

			_, err := executor.Execute(
				context.Background(),
				testCase.calls,
			)

			var failure *toolFailure

			if !errors.As(err, &failure) ||
				failure.Code != testCase.wantCode {
				t.Fatalf("unexpected error: %v", err)
			}

			if runs != testCase.wantRuns {
				t.Fatalf(
					"tool ran %d times, expected %d",
					runs,
					testCase.wantRuns,
				)
			}
		})
	}
}

func TestStep08CanceledSessionWait(t *testing.T) {
	service := NewService(nil, nil)

	lock := make(chan struct{}, 1)
	lock <- struct{}{}

	service.turnLocks.Store("busy-session", lock)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		20*time.Millisecond,
	)
	defer cancel()

	_, err := service.Reply(
		ctx,
		"user-test",
		"busy-session",
		"你好",
	)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}

	if len(lock) != 1 {
		t.Fatal("waiting request changed another turn's lock")
	}
}
