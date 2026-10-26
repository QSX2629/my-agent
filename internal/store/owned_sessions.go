package store

import (
	"context"
	"database/sql"
	"time"

	"my-agent/internal/model"
)

// MigrateSessionOwners 给会话表补充 user_id 字段。
// 应在启动 HTTP 服务前调用，可以重复执行。
func (s *Store) MigrateSessionOwners() error {
	rows, err := s.db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		return err
	}

	hasUserID := false

	for rows.Next() {
		var columnID int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var primaryKey int

		if err := rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			rows.Close()
			return err
		}

		if name == "user_id" {
			hasUserID = true
		}
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}

	// 原来的 Store 只允许一个连接，
	// 必须先关闭查询结果，再执行下面的 SQL。
	if err := rows.Close(); err != nil {
		return err
	}

	if !hasUserID {
		_, err := s.db.Exec(`
			ALTER TABLE sessions
			ADD COLUMN user_id TEXT NOT NULL DEFAULT ''
		`)
		if err != nil {
			return err
		}
	}

	_, err = s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sessions_owner_updated
		ON sessions(user_id, updated_at)
	`)

	return err
}

func (s *Store) CreateOwnedSession(
	ctx context.Context,
	userID string,
	sessionID string,
	title string,
) (Session, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	session := Session{
		ID:        sessionID,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (
			id, title, created_at, updated_at, user_id
		)
		VALUES (?, ?, ?, ?, ?)
	`,
		session.ID,
		session.Title,
		session.CreatedAt,
		session.UpdatedAt,
		userID,
	)

	return session, err
}

func (s *Store) ListOwnedSessions(
	ctx context.Context,
	userID string,
) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, created_at, updated_at
		FROM sessions
		WHERE user_id = ?
		ORDER BY updated_at DESC, id DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 初始化为空切片，让 JSON 返回 []。
	sessions := make([]Session, 0)

	for rows.Next() {
		var session Session

		if err := rows.Scan(
			&session.ID,
			&session.Title,
			&session.CreatedAt,
			&session.UpdatedAt,
		); err != nil {
			return nil, err
		}

		sessions = append(sessions, session)
	}

	return sessions, rows.Err()
}

func (s *Store) LoadOwnedMessages(
	ctx context.Context,
	userID string,
	sessionID string,
) ([]model.Message, error) {
	// 先确认会话存在，而且属于当前用户。
	// 不存在或属于其他用户，都会返回 sql.ErrNoRows。
	var id string

	err := s.db.QueryRowContext(ctx, `
		SELECT id
		FROM sessions
		WHERE id = ? AND user_id = ?
	`, sessionID, userID).Scan(&id)

	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT m.role, m.content
		FROM messages AS m
		JOIN sessions AS s ON s.id = m.session_id
		WHERE s.id = ? AND s.user_id = ?
		ORDER BY m.id ASC
	`, sessionID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]model.Message, 0)

	for rows.Next() {
		var message model.Message

		if err := rows.Scan(
			&message.Role,
			&message.Content,
		); err != nil {
			return nil, err
		}

		messages = append(messages, message)
	}

	return messages, rows.Err()
}

// SaveOwnedTurn 在同一个事务中保存一轮对话。
// 两条消息要么都保存成功，要么都不保存。
func (s *Store) SaveOwnedTurn(
	ctx context.Context,
	userID string,
	sessionID string,
	userMessage string,
	assistantMessage string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// 再次检查归属，避免模型回答期间会话已经被删除。
	result, err := tx.ExecContext(ctx, `
		UPDATE sessions
		SET updated_at = ?
		WHERE id = ? AND user_id = ?
	`, now, sessionID, userID)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return sql.ErrNoRows
	}

	turn := []model.Message{
		{Role: "user", Content: userMessage},
		{Role: "assistant", Content: assistantMessage},
	}

	for _, message := range turn {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO messages (
				session_id, role, content, created_at
			)
			VALUES (?, ?, ?, ?)
		`,
			sessionID,
			message.Role,
			message.Content,
			now,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) DeleteOwnedSession(
	ctx context.Context,
	userID string,
	sessionID string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 依次删除摘要、提示词选择和消息。
	// 每一条 SQL 都检查会话是否属于当前用户。
	queries := []string{
		`
			DELETE FROM session_memory
			WHERE session_id IN (
				SELECT id
				FROM sessions
				WHERE id = ? AND user_id = ?
			)
		`,
		`
			DELETE FROM session_prompts
			WHERE session_id IN (
				SELECT id
				FROM sessions
				WHERE id = ? AND user_id = ?
			)
		`,
		`
			DELETE FROM messages
			WHERE session_id IN (
				SELECT id
				FROM sessions
				WHERE id = ? AND user_id = ?
			)
		`,
	}

	for _, query := range queries {
		if _, err := tx.ExecContext(
			ctx,
			query,
			sessionID,
			userID,
		); err != nil {
			return err
		}
	}

	result, err := tx.ExecContext(ctx, `
		DELETE FROM sessions
		WHERE id = ? AND user_id = ?
	`, sessionID, userID)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return sql.ErrNoRows
	}

	return tx.Commit()
}
