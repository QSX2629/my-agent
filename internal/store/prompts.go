package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalidPrompt  = errors.New("invalid prompt")
	ErrPromptNotFound = errors.New("prompt not found")
)

// Prompt 是用户保存的一个提示词。
type Prompt struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// SessionPrompt 表示某个会话当前选择的提示词。
// Prompt 为 nil 时，使用默认提示词。
type SessionPrompt struct {
	SessionID string  `json:"sessionId"`
	Prompt    *Prompt `json:"prompt"`
}

// createPromptTables 创建提示词和会话选择关系表。
// 可以在每次启动时重复执行。
func (s *Store) createPromptTables() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS prompts (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_prompts_owner_updated
		ON prompts(user_id, updated_at);

		CREATE TABLE IF NOT EXISTS session_prompts (
			session_id TEXT PRIMARY KEY,
			prompt_id TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_session_prompts_prompt
		ON session_prompts(prompt_id);
	`)

	return err
}

func normalizePrompt(
	name string,
	content string,
) (string, string, error) {
	name = strings.TrimSpace(name)
	content = strings.TrimSpace(content)

	nameLength := utf8.RuneCountInString(name)
	contentLength := utf8.RuneCountInString(content)

	if nameLength < 1 || nameLength > 100 {
		return "", "", ErrInvalidPrompt
	}

	if contentLength < 1 || contentLength > 20000 {
		return "", "", ErrInvalidPrompt
	}

	return name, content, nil
}

// CreateOwnedPrompt 创建当前用户的提示词。
func (s *Store) CreateOwnedPrompt(
	ctx context.Context,
	userID string,
	name string,
	content string,
) (Prompt, error) {
	name, content, err := normalizePrompt(name, content)
	if err != nil {
		return Prompt{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	prompt := Prompt{
		ID:        uuid.New().String(),
		Name:      name,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO prompts (
			id, user_id, name, content, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
	`,
		prompt.ID,
		userID,
		prompt.Name,
		prompt.Content,
		prompt.CreatedAt,
		prompt.UpdatedAt,
	)

	return prompt, err
}

// ListOwnedPrompts 查询当前用户的全部提示词。
func (s *Store) ListOwnedPrompts(
	ctx context.Context,
	userID string,
) ([]Prompt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, content, created_at, updated_at
		FROM prompts
		WHERE user_id = ?
		ORDER BY updated_at DESC, id DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	prompts := make([]Prompt, 0)

	for rows.Next() {
		var prompt Prompt

		if err := rows.Scan(
			&prompt.ID,
			&prompt.Name,
			&prompt.Content,
			&prompt.CreatedAt,
			&prompt.UpdatedAt,
		); err != nil {
			return nil, err
		}

		prompts = append(prompts, prompt)
	}

	return prompts, rows.Err()
}

// GetOwnedPrompt 查询当前用户的一个提示词。
func (s *Store) GetOwnedPrompt(
	ctx context.Context,
	userID string,
	promptID string,
) (Prompt, error) {
	var prompt Prompt

	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, content, created_at, updated_at
		FROM prompts
		WHERE id = ? AND user_id = ?
	`, promptID, userID).Scan(
		&prompt.ID,
		&prompt.Name,
		&prompt.Content,
		&prompt.CreatedAt,
		&prompt.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return Prompt{}, ErrPromptNotFound
	}

	return prompt, err
}

// UpdateOwnedPrompt 完整更新提示词的名称和内容。
func (s *Store) UpdateOwnedPrompt(
	ctx context.Context,
	userID string,
	promptID string,
	name string,
	content string,
) (Prompt, error) {
	name, content, err := normalizePrompt(name, content)
	if err != nil {
		return Prompt{}, err
	}

	var prompt Prompt

	err = s.db.QueryRowContext(ctx, `
		UPDATE prompts
		SET name = ?, content = ?, updated_at = ?
		WHERE id = ? AND user_id = ?
		RETURNING id, name, content, created_at, updated_at
	`,
		name,
		content,
		time.Now().UTC().Format(time.RFC3339Nano),
		promptID,
		userID,
	).Scan(
		&prompt.ID,
		&prompt.Name,
		&prompt.Content,
		&prompt.CreatedAt,
		&prompt.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return Prompt{}, ErrPromptNotFound
	}

	return prompt, err
}

// DeleteOwnedPrompt 解除关联，并删除当前用户的提示词。
func (s *Store) DeleteOwnedPrompt(
	ctx context.Context,
	userID string,
	promptID string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 只有当前用户拥有这个提示词时，才清理相关选择。
	_, err = tx.ExecContext(ctx, `
		DELETE FROM session_prompts
		WHERE prompt_id IN (
			SELECT id
			FROM prompts
			WHERE id = ? AND user_id = ?
		)
	`, promptID, userID)
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		DELETE FROM prompts
		WHERE id = ? AND user_id = ?
	`, promptID, userID)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return ErrPromptNotFound
	}

	return tx.Commit()
}

// GetSessionPrompt 查询当前用户某个会话选择的提示词。
func (s *Store) GetSessionPrompt(
	ctx context.Context,
	userID string,
	sessionID string,
) (SessionPrompt, error) {
	result := SessionPrompt{
		SessionID: sessionID,
	}

	// 会话存在但没有选择提示词时，关联字段会是 NULL。
	var id sql.NullString
	var name sql.NullString
	var content sql.NullString
	var createdAt sql.NullString
	var updatedAt sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT
			s.id,
			p.id,
			p.name,
			p.content,
			p.created_at,
			p.updated_at
		FROM sessions AS s
		LEFT JOIN session_prompts AS sp
			ON sp.session_id = s.id
		LEFT JOIN prompts AS p
			ON p.id = sp.prompt_id
			AND p.user_id = s.user_id
		WHERE s.id = ? AND s.user_id = ?
	`, sessionID, userID).Scan(
		&result.SessionID,
		&id,
		&name,
		&content,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return result, err
	}

	if id.Valid {
		result.Prompt = &Prompt{
			ID:        id.String,
			Name:      name.String,
			Content:   content.String,
			CreatedAt: createdAt.String,
			UpdatedAt: updatedAt.String,
		}
	}

	return result, nil
}

// SetSessionPrompt 为当前用户的会话选择提示词。
// promptID 为空时，取消选择，恢复默认提示词。
func (s *Store) SetSessionPrompt(
	ctx context.Context,
	userID string,
	sessionID string,
	promptID string,
) (SessionPrompt, error) {
	result := SessionPrompt{
		SessionID: sessionID,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	// 在事务中验证会话归属，并更新会话时间。
	updateResult, err := tx.ExecContext(ctx, `
		UPDATE sessions
		SET updated_at = ?
		WHERE id = ? AND user_id = ?
	`,
		time.Now().UTC().Format(time.RFC3339Nano),
		sessionID,
		userID,
	)
	if err != nil {
		return result, err
	}

	count, err := updateResult.RowsAffected()
	if err != nil {
		return result, err
	}

	if count == 0 {
		return result, sql.ErrNoRows
	}

	if promptID == "" {
		_, err = tx.ExecContext(ctx, `
			DELETE FROM session_prompts
			WHERE session_id = ?
		`, sessionID)
	} else {
		var prompt Prompt

		// 提示词也必须属于当前用户。
		err = tx.QueryRowContext(ctx, `
			SELECT id, name, content, created_at, updated_at
			FROM prompts
			WHERE id = ? AND user_id = ?
		`, promptID, userID).Scan(
			&prompt.ID,
			&prompt.Name,
			&prompt.Content,
			&prompt.CreatedAt,
			&prompt.UpdatedAt,
		)

		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrPromptNotFound
		}
		if err != nil {
			return result, err
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO session_prompts (
				session_id, prompt_id
			)
			VALUES (?, ?)
			ON CONFLICT(session_id)
			DO UPDATE SET prompt_id = excluded.prompt_id
		`, sessionID, promptID)

		result.Prompt = &prompt
	}

	if err != nil {
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}

	return result, nil
}
