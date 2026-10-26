package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"my-agent/internal/model"
	"my-agent/internal/store"
)

var ErrModelCall = errors.New("model call failed")

const defaultSystemPrompt = "你是一个有帮助的中文助手。"

type Service struct {
	modelClient *model.Client
	store       *store.Store

	// 每个会话对应一个容量为 1 的通道，
	// 用来保证同一会话的聊天按顺序执行。
	turnLocks sync.Map
}

func NewService(
	modelClient *model.Client,
	messageStore *store.Store,
) *Service {
	return &Service{
		modelClient: modelClient,
		store:       messageStore,
	}
}

func (s *Service) Reply(
	ctx context.Context,
	userID string,
	sessionID string,
	userMessage string,
) (string, error) {
	// 整轮请求共享这一份时间预算。
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	lockValue, _ := s.turnLocks.LoadOrStore(
		sessionID,
		make(chan struct{}, 1),
	)

	lock := lockValue.(chan struct{})

	// 等待会话执行资格时，也能响应取消和超时。
	select {
	case lock <- struct{}{}:
		defer func() {
			<-lock
		}()

	case <-ctx.Done():
		return "", ctx.Err()
	}

	// 避免取得执行资格时，请求恰好已经过期。
	if err := ctx.Err(); err != nil {
		return "", err
	}

	memory, history, err := s.store.LoadOwnedContext(
		ctx,
		userID,
		sessionID,
	)
	if err != nil {
		return "", fmt.Errorf("load context: %w", err)
	}

	selection, err := s.store.GetSessionPrompt(
		ctx,
		userID,
		sessionID,
	)
	if err != nil {
		return "", fmt.Errorf("load session prompt: %w", err)
	}

	systemPrompt := defaultSystemPrompt

	if selection.Prompt != nil {
		systemPrompt = selection.Prompt.Content
	}

	messages, memory, err := s.prepareContext(
		ctx,
		systemPrompt,
		memory,
		history,
		userMessage,
	)
	if err != nil {
		return "", fmt.Errorf("prepare context: %w", err)
	}

	scope := toolScope{
		runID:     uuid.NewString(),
		userID:    userID,
		sessionID: sessionID,
	}

	reply, err := s.replyWithTools(
		ctx,
		scope,
		messages,
	)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrModelCall, err)
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	if err := s.store.SaveOwnedTurnWithMemory(
		ctx,
		userID,
		sessionID,
		userMessage,
		reply,
		memory,
	); err != nil {
		return "", fmt.Errorf("save turn and memory: %w", err)
	}

	return reply, nil
}
