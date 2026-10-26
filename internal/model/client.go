package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

// 对外提供固定错误说明，同时保留底层错误。
type requestError struct {
	operation string
	cause     error
}

func (e *requestError) Error() string {
	return e.operation
}

func (e *requestError) Unwrap() error {
	return e.cause
}

func NewClient(apiKey, baseURL, modelName string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   modelName,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Chat 用于普通文本生成，例如历史摘要。
// 此方法不向模型提供工具。
func (c *Client) Chat(
	ctx context.Context,
	messages []Message,
) (string, error) {
	einoMessages := make([]*schema.Message, 0, len(messages))

	for index, message := range messages {
		role := schema.RoleType(message.Role)

		switch role {
		case schema.System, schema.User, schema.Assistant:
			einoMessages = append(einoMessages, &schema.Message{
				Role:    role,
				Content: message.Content,
			})

		default:
			return "", fmt.Errorf(
				"unsupported model message role at index %d",
				index,
			)
		}
	}

	response, err := c.Generate(ctx, einoMessages, nil)
	if err != nil {
		return "", err
	}

	// 普通文本生成不能把工具调用请求当作摘要或回答。
	if len(response.ToolCalls) > 0 {
		return "", errors.New(
			"unexpected tool calls in text generation",
		)
	}

	return response.Content, nil
}

// Generate 返回完整的 Eino 消息。
// 返回内容可能是文本，也可能包含工具调用请求。
func (c *Client) Generate(
	ctx context.Context,
	messages []*schema.Message,
	toolInfos []*schema.ToolInfo,
) (*schema.Message, error) {
	if len(messages) == 0 {
		return nil, errors.New("model messages are empty")
	}

	httpClient := c.HTTPClient

	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 60 * time.Second,
		}
	}

	callCtx := ctx

	if httpClient.Timeout > 0 {
		var cancel context.CancelFunc

		callCtx, cancel = context.WithTimeout(
			ctx,
			httpClient.Timeout,
		)
		defer cancel()
	}

	if err := callCtx.Err(); err != nil {
		return nil, wrapRequestError(
			callCtx,
			"model request canceled or timed out",
			err,
		)
	}

	baseModel, err := einoopenai.NewChatModel(
		callCtx,
		&einoopenai.ChatModelConfig{
			APIKey:     c.APIKey,
			BaseURL:    c.BaseURL,
			Model:      c.Model,
			HTTPClient: httpClient,
		},
	)
	if err != nil {
		return nil, wrapRequestError(
			callCtx,
			"initialize Eino model failed",
			err,
		)
	}

	var chatModel einomodel.ToolCallingChatModel = baseModel

	// 把工具的名称、用途和参数说明提供给模型。
	if len(toolInfos) > 0 {
		chatModel, err = chatModel.WithTools(toolInfos)
		if err != nil {
			return nil, wrapRequestError(
				callCtx,
				"bind model tools failed",
				err,
			)
		}
	}

	response, err := chatModel.Generate(
		callCtx,
		messages,
	)
	if err != nil {
		return nil, wrapRequestError(
			callCtx,
			"model generation failed",
			err,
		)
	}

	if response == nil {
		return nil, errors.New("model response is nil")
	}

	if response.Role != schema.Assistant {
		return nil, errors.New(
			"model response has an invalid role",
		)
	}

	// 工具调用请求可以没有文本。
	// 如果也没有工具调用，则必须有正常的文本回答。
	if len(response.ToolCalls) == 0 &&
		strings.TrimSpace(response.Content) == "" {
		return nil, errors.New(
			"model response contained no text",
		)
	}

	return response, nil
}

func wrapRequestError(
	ctx context.Context,
	operation string,
	cause error,
) error {
	if err := ctx.Err(); err != nil {
		cause = err
	}

	return &requestError{
		operation: operation,
		cause:     cause,
	}
}
