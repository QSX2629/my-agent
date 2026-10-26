package agent

import (
	"context"
	"log/slog"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"my-agent/internal/model"
	"my-agent/internal/tools"
)

func (s *Service) replyWithTools(
	ctx context.Context,
	scope toolScope,
	messages []model.Message,
) (string, error) {
	input := make([]*schema.Message, 0, len(messages))

	for _, message := range messages {
		input = append(input, &schema.Message{
			Role:    schema.RoleType(message.Role),
			Content: message.Content,
		})
	}

	// 只读取当前登录用户已启用的 Skill。
	documents, err := s.store.ListOwnedSkills(
		ctx,
		scope.userID,
		true,
	)
	if err != nil {
		return "", err
	}

	builtins := []einotool.InvokableTool{
		tools.NewCurrentTime(),
	}

	// 没有启用的 Skill 时，保持只有时间工具。
	if len(documents) > 0 {
		builtins = append(
			builtins,
			tools.NewLoadSkill(documents),
		)
	}

	executor, err := newToolExecutor(
		ctx,
		scope,
		builtins,
		slog.Default(),
	)
	if err != nil {
		return "", err
	}

	for {
		response, err := s.modelClient.Generate(
			ctx,
			input,
			executor.definitions,
		)
		if err != nil {
			return "", err
		}

		if err := ctx.Err(); err != nil {
			return "", err
		}

		if len(response.ToolCalls) == 0 {
			return response.Content, nil
		}

		results, err := executor.Execute(
			ctx,
			response.ToolCalls,
		)
		if err != nil {
			return "", err
		}

		input = append(input, response)
		input = append(input, results...)
	}
}
