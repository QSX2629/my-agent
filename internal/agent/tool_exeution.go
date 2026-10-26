package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// 整轮请求的时间预算，包括：
// 等待会话锁、读取历史、摘要、模型调用、工具执行和保存。
const turnTimeout = 2 * time.Minute

type toolLimits struct {
	Timeout          time.Duration
	MaxCalls         int
	MaxArgumentBytes int
	MaxResultBytes   int
	MaxCallIDBytes   int
}

// 这些是教学默认值，不是 Eino 或千问的官方限制。
func defaultToolLimits() toolLimits {
	return toolLimits{
		Timeout:          5 * time.Second,
		MaxCalls:         4,
		MaxArgumentBytes: 4 * 1024,
		MaxResultBytes:   8 * 1024,
		MaxCallIDBytes:   128,
	}
}

// 这些信息由后端提供，用于关联审计日志。
type toolScope struct {
	runID     string
	userID    string
	sessionID string
}

// 对外只展示固定错误码，同时保留底层错误。
// 因此 errors.Is 仍能识别取消和超时。
type toolFailure struct {
	Code  string
	cause error
}

func (e *toolFailure) Error() string {
	return "tool execution failed: " + e.Code
}

func (e *toolFailure) Unwrap() error {
	return e.cause
}

// 每轮聊天创建一个执行器。
// 调用次数、调用 ID 和执行限制都属于当前这一轮。
type toolExecutor struct {
	scope       toolScope
	limits      toolLimits
	logger      *slog.Logger
	tools       map[string]einotool.InvokableTool
	definitions []*schema.ToolInfo
	used        int
	seenIDs     map[string]struct{}
}

func newToolExecutor(
	ctx context.Context,
	scope toolScope,
	builtins []einotool.InvokableTool,
	logger *slog.Logger,
) (*toolExecutor, error) {
	if logger == nil {
		logger = slog.Default()
	}

	executor := &toolExecutor{
		scope:   scope,
		limits:  defaultToolLimits(),
		logger:  logger,
		tools:   make(map[string]einotool.InvokableTool),
		seenIDs: make(map[string]struct{}),
	}

	for _, builtin := range builtins {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		info, err := builtin.Info(ctx)
		if err != nil {
			return nil, &toolFailure{
				Code:  "INVALID_TOOL_DEFINITION",
				cause: err,
			}
		}

		if info == nil || info.Name == "" {
			return nil, &toolFailure{
				Code: "INVALID_TOOL_DEFINITION",
			}
		}

		if _, exists := executor.tools[info.Name]; exists {
			return nil, &toolFailure{
				Code: "INVALID_TOOL_DEFINITION",
			}
		}

		executor.tools[info.Name] = builtin
		executor.definitions = append(
			executor.definitions,
			info,
		)
	}

	return executor, nil
}

// Execute 先检查整批调用，再逐个执行。
// 避免前面的工具已经执行，才发现后面存在明显无效的请求。
func (e *toolExecutor) Execute(
	ctx context.Context,
	calls []schema.ToolCall,
) ([]*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if e.used+len(calls) > e.limits.MaxCalls {
		e.logger.Info(
			"agent.tool.batch_rejected",
			"runId", e.scope.runID,
			"userId", e.scope.userID,
			"sessionId", e.scope.sessionID,
			"status", "rejected",
			"errorCode", "TOOL_CALL_LIMIT",
			"requestedCalls", len(calls),
			"usedCalls", e.used,
		)

		return nil, &toolFailure{
			Code: "TOOL_CALL_LIMIT",
		}
	}

	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" ||
			len(call.ID) > e.limits.MaxCallIDBytes {
			return nil, e.reject(
				call,
				"INVALID_TOOL_CALL_ID",
			)
		}

		if _, exists := e.seenIDs[call.ID]; exists {
			return nil, e.reject(
				call,
				"DUPLICATE_TOOL_CALL_ID",
			)
		}

		e.seenIDs[call.ID] = struct{}{}

		if _, exists := e.tools[call.Function.Name]; !exists {
			return nil, e.reject(call, "UNKNOWN_TOOL")
		}

		if len(call.Function.Arguments) >
			e.limits.MaxArgumentBytes {
			return nil, e.reject(
				call,
				"TOOL_ARGUMENTS_TOO_LARGE",
			)
		}

		arguments := strings.TrimSpace(
			call.Function.Arguments,
		)

		if arguments == "" {
			arguments = "{}"
		}

		// 先检查参数是不是合法 JSON 对象。
		// 具体字段是否符合要求，仍由各个工具检查。
		var parameters map[string]json.RawMessage

		if err := json.Unmarshal(
			[]byte(arguments),
			&parameters,
		); err != nil || parameters == nil {
			return nil, e.reject(
				call,
				"INVALID_TOOL_ARGUMENTS",
			)
		}
	}

	results := make([]*schema.Message, 0, len(calls))

	for _, call := range calls {
		result, err := e.invoke(ctx, call)
		if err != nil {
			return nil, err
		}

		results = append(results, &schema.Message{
			Role:       schema.Tool,
			Content:    result,
			ToolCallID: call.ID,
		})
	}

	return results, nil
}

func (e *toolExecutor) invoke(
	ctx context.Context,
	call schema.ToolCall,
) (result string, err error) {
	startedAt := time.Now()
	resultBytes := 0

	e.audit(call, "started", "", startedAt, 0)

	// 无论正常返回、错误还是 panic，都记录结束状态。
	defer func() {
		if recover() != nil {
			result = ""
			err = &toolFailure{
				Code: "TOOL_PANIC",
			}
		}

		status := "completed"
		code := ""

		if err != nil {
			status = "failed"
			code = "TOOL_FAILED"

			var failure *toolFailure

			if errors.As(err, &failure) {
				code = failure.Code
			}
		}

		e.audit(
			call,
			status,
			code,
			startedAt,
			resultBytes,
		)
	}()

	toolCtx, cancel := context.WithTimeout(
		ctx,
		e.limits.Timeout,
	)
	defer cancel()

	if err = toolCtx.Err(); err == nil {
		// 统计实际开始执行的次数。
		e.used++

		selected := e.tools[call.Function.Name]

		result, err = selected.InvokableRun(
			toolCtx,
			call.Function.Arguments,
		)
	}

	resultBytes = len(result)

	// 必须在 cancel 执行之前检查。
	// 即使工具忽略了取消，并且最终返回，也拒绝过期结果。
	if contextErr := toolCtx.Err(); contextErr != nil {
		err = contextErr
	}

	if err != nil {
		code := "TOOL_FAILED"

		switch {
		case errors.Is(err, context.DeadlineExceeded):
			code = "TOOL_TIMEOUT"

		case errors.Is(err, context.Canceled):
			code = "TOOL_CANCELED"
		}

		return "", &toolFailure{
			Code:  code,
			cause: err,
		}
	}

	if resultBytes > e.limits.MaxResultBytes {
		return "", &toolFailure{
			Code: "TOOL_RESULT_TOO_LARGE",
		}
	}

	return result, nil
}

func (e *toolExecutor) reject(
	call schema.ToolCall,
	code string,
) error {
	e.audit(call, "rejected", code, time.Now(), 0)

	return &toolFailure{
		Code: code,
	}
}

func (e *toolExecutor) audit(
	call schema.ToolCall,
	status string,
	code string,
	startedAt time.Time,
	resultBytes int,
) {
	e.logger.Info(
		"agent.tool.audit",
		"runId", e.scope.runID,
		"userId", e.scope.userID,
		"sessionId", e.scope.sessionID,
		"tool", shortLogValue(call.Function.Name, 64),
		"callId", shortLogValue(call.ID, 128),
		"status", status,
		"durationMs", time.Since(startedAt).Milliseconds(),
		"argumentBytes", len(call.Function.Arguments),
		"resultBytes", resultBytes,
		"errorCode", code,
	)
}

func shortLogValue(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit]
	}

	return strings.ToValidUTF8(value, "?")
}
