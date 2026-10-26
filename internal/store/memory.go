package store

import (
	"context"
	"database/sql"
	"time"

	"my-agent/internal/model"
)

// ConversationMemory 保存旧对话摘要及其覆盖位置。
type ConversationMemory struct {
	Summary   string
	ThroughID int64
}

// ContextMessage 比普通消息多一个数据库 ID。
// ID 用于记录摘要已经处理到哪条消息，不发给模型。
type ContextMessage struct {
	ID      int64  `json:"-"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (s *Store) createMemoryTables() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS session_memory (
			session_id TEXT PRIMARY KEY,
			summary TEXT NOT NULL,
			through_id INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);
	`)

	return err
}

// LoadOwnedContext 加载摘要，以及尚未被摘要覆盖的消息。
func (s *Store) LoadOwnedContext(
	ctx context.Context,
	userID string,
	sessionID string,
) (ConversationMemory, []ContextMessage, error) {
	var memory ConversationMemory
	history := make([]ContextMessage, 0)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory, nil, err
	}
	defer tx.Rollback()

	// 同时检查会话归属并读取摘要。
	// 会话存在但没有摘要时，得到空字符串和 0。
	err = tx.QueryRowContext(ctx, `
		SELECT
			COALESCE(sm.summary, ''),
			COALESCE(sm.through_id, 0)
		FROM sessions AS s
		LEFT JOIN session_memory AS sm
			ON sm.session_id = s.id
		WHERE s.id = ? AND s.user_id = ?
	`, sessionID, userID).Scan(
		&memory.Summary,
		&memory.ThroughID,
	)
	if err != nil {
		return memory, nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT m.id, m.role, m.content
		FROM messages AS m
		JOIN sessions AS s ON s.id = m.session_id
		WHERE s.id = ?
			AND s.user_id = ?
			AND m.id > ?
		ORDER BY m.id ASC
	`, sessionID, userID, memory.ThroughID)
	if err != nil {
		return memory, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var message ContextMessage

		if err := rows.Scan(
			&message.ID,
			&message.Role,
			&message.Content,
		); err != nil {
			return memory, nil, err
		}

		history = append(history, message)
	}

	if err := rows.Err(); err != nil {
		return memory, nil, err
	}

	if err := rows.Close(); err != nil {
		return memory, nil, err
	}

	if err := tx.Commit(); err != nil {
		return memory, nil, err
	}

	return memory, history, nil
}

// SaveOwnedTurnWithMemory 在同一个事务中保存：
// 用户消息、助手回答、摘要及摘要位置。
func (s *Store) SaveOwnedTurnWithMemory(
	ctx context.Context,
	userID string,
	sessionID string,
	userMessage string,
	assistantMessage string,
	memory ConversationMemory,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339Nano)

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

	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_memory (
			session_id, summary, through_id, updated_at
		)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id)
		DO UPDATE SET
			summary = excluded.summary,
			through_id = excluded.through_id,
			updated_at = excluded.updated_at
	`,
		sessionID,
		memory.Summary,
		memory.ThroughID,
		now,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}
