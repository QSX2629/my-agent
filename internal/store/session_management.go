package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"my-agent/internal/model"
)

var (
	ErrInvalidTitle      = errors.New("invalid session title")
	ErrInvalidPagination = errors.New("invalid pagination")
)

// SessionPage 是会话列表的分页结果。
type SessionPage struct {
	Items    []Session `json:"items"`
	Total    int64     `json:"total"`
	Page     int64     `json:"page"`
	PageSize int64     `json:"pageSize"`
}

// MessagePage 是某个会话的消息分页结果。
type MessagePage struct {
	SessionID string          `json:"sessionId"`
	Messages  []model.Message `json:"messages"`
	Total     int64           `json:"total"`
	Page      int64           `json:"page"`
	PageSize  int64           `json:"pageSize"`
}

// paginationOffset 检查分页参数，并计算需要跳过多少条记录。
func paginationOffset(page, pageSize int64) (int64, error) {
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return 0, ErrInvalidPagination
	}

	// 防止极大的页码导致乘法溢出。
	if page-1 > math.MaxInt64/pageSize {
		return 0, ErrInvalidPagination
	}

	return (page - 1) * pageSize, nil
}

// RenameOwnedSession 修改当前用户自己的会话标题。
func (s *Store) RenameOwnedSession(
	ctx context.Context,
	userID string,
	sessionID string,
	title string,
) (Session, error) {
	title = strings.TrimSpace(title)

	length := utf8.RuneCountInString(title)

	if length < 1 || length > 100 {
		return Session{}, ErrInvalidTitle
	}

	var session Session

	err := s.db.QueryRowContext(ctx, `
		UPDATE sessions
		SET title = ?, updated_at = ?
		WHERE id = ? AND user_id = ?
		RETURNING id, title, created_at, updated_at
	`,
		title,
		time.Now().UTC().Format(time.RFC3339Nano),
		sessionID,
		userID,
	).Scan(
		&session.ID,
		&session.Title,
		&session.CreatedAt,
		&session.UpdatedAt,
	)

	return session, err
}

// ListOwnedSessionsPage 按页查询当前用户的会话。
func (s *Store) ListOwnedSessionsPage(
	ctx context.Context,
	userID string,
	page int64,
	pageSize int64,
) (SessionPage, error) {
	result := SessionPage{
		Items:    make([]Session, 0),
		Page:     page,
		PageSize: pageSize,
	}

	offset, err := paginationOffset(page, pageSize)
	if err != nil {
		return result, err
	}

	// 总数和当前页在同一个事务中读取。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM sessions
		WHERE user_id = ?
	`, userID).Scan(&result.Total)

	if err != nil {
		return result, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, title, created_at, updated_at
		FROM sessions
		WHERE user_id = ?
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, userID, pageSize, offset)

	if err != nil {
		return result, err
	}
	defer rows.Close()

	for rows.Next() {
		var session Session

		if err := rows.Scan(
			&session.ID,
			&session.Title,
			&session.CreatedAt,
			&session.UpdatedAt,
		); err != nil {
			return result, err
		}

		result.Items = append(result.Items, session)
	}

	if err := rows.Err(); err != nil {
		return result, err
	}

	// 先关闭查询结果，再提交事务。
	if err := rows.Close(); err != nil {
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}

	return result, nil
}

// LoadOwnedMessagesPage 按页查询当前用户某个会话的消息。
func (s *Store) LoadOwnedMessagesPage(
	ctx context.Context,
	userID string,
	sessionID string,
	page int64,
	pageSize int64,
) (MessagePage, error) {
	result := MessagePage{
		SessionID: sessionID,
		Messages:  make([]model.Message, 0),
		Page:      page,
		PageSize:  pageSize,
	}

	offset, err := paginationOffset(page, pageSize)
	if err != nil {
		return result, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	// 先检查会话是否属于当前用户。
	// 不存在或属于其他用户，都返回 sql.ErrNoRows。
	var id string

	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM sessions
		WHERE id = ? AND user_id = ?
	`, sessionID, userID).Scan(&id)

	if err != nil {
		return result, err
	}

	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM messages AS m
		JOIN sessions AS s ON s.id = m.session_id
		WHERE s.id = ? AND s.user_id = ?
	`, sessionID, userID).Scan(&result.Total)

	if err != nil {
		return result, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT m.role, m.content
		FROM messages AS m
		JOIN sessions AS s ON s.id = m.session_id
		WHERE s.id = ? AND s.user_id = ?
		ORDER BY m.id ASC
		LIMIT ? OFFSET ?
	`, sessionID, userID, pageSize, offset)

	if err != nil {
		return result, err
	}
	defer rows.Close()

	for rows.Next() {
		var message model.Message

		if err := rows.Scan(
			&message.Role,
			&message.Content,
		); err != nil {
			return result, err
		}

		result.Messages = append(result.Messages, message)
	}

	if err := rows.Err(); err != nil {
		return result, err
	}

	if err := rows.Close(); err != nil {
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}

	return result, nil
}
