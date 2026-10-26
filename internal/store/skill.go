package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"my-agent/internal/skill"
)

var ErrSkillLimit = errors.New("skill limit reached")

func (s *Store) createSkillTables() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS user_skills (
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL,
			content TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1
				CHECK(enabled IN (0, 1)),
			updated_at TEXT NOT NULL,

			PRIMARY KEY(user_id, name)
		);
	`)

	return err
}

func (s *Store) SaveOwnedSkill(
	ctx context.Context,
	userID string,
	filename string,
	data []byte,
) (skill.Document, error) {
	document, err := skill.Parse(filename, data)
	if err != nil {
		return skill.Document{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return skill.Document{}, err
	}
	defer tx.Rollback()

	// 排除当前名称，允许在满额时更新已有 Skill。
	var others int

	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM user_skills
		WHERE user_id = ? AND name <> ?
	`, userID, document.Name).Scan(&others)

	if err != nil {
		return skill.Document{}, err
	}

	if others >= skill.MaxSkills {
		return skill.Document{}, ErrSkillLimit
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// 新建时默认启用。
	// 同名更新时保留原来的启用状态。
	err = tx.QueryRowContext(ctx, `
		INSERT INTO user_skills (
			user_id,
			name,
			description,
			content,
			enabled,
			updated_at
		)
		VALUES (?, ?, ?, ?, 1, ?)

		ON CONFLICT(user_id, name) DO UPDATE SET
			description = excluded.description,
			content = excluded.content,
			updated_at = excluded.updated_at

		RETURNING
			name,
			description,
			content,
			enabled,
			updated_at
	`,
		userID,
		document.Name,
		document.Description,
		document.Content,
		now,
	).Scan(
		&document.Name,
		&document.Description,
		&document.Content,
		&document.Enabled,
		&document.UpdatedAt,
	)

	if err != nil {
		return skill.Document{}, err
	}

	if err := tx.Commit(); err != nil {
		return skill.Document{}, err
	}

	return document, nil
}

func (s *Store) ListOwnedSkills(
	ctx context.Context,
	userID string,
	enabledOnly bool,
) ([]skill.Document, error) {
	query := `
		SELECT
			name,
			description,
			content,
			enabled,
			updated_at
		FROM user_skills
		WHERE user_id = ?
	`

	if enabledOnly {
		query += " AND enabled = 1"
	}

	query += " ORDER BY name ASC"

	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	documents := make([]skill.Document, 0)

	for rows.Next() {
		var document skill.Document

		if err := rows.Scan(
			&document.Name,
			&document.Description,
			&document.Content,
			&document.Enabled,
			&document.UpdatedAt,
		); err != nil {
			return nil, err
		}

		documents = append(documents, document)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return documents, nil
}

func (s *Store) SetOwnedSkillEnabled(
	ctx context.Context,
	userID string,
	name string,
	enabled bool,
) error {
	if !skill.ValidName(name) {
		return skill.ErrInvalid
	}

	result, err := s.db.ExecContext(ctx, `
		UPDATE user_skills
		SET enabled = ?, updated_at = ?
		WHERE user_id = ? AND name = ?
	`,
		enabled,
		time.Now().UTC().Format(time.RFC3339Nano),
		userID,
		name,
	)

	if err != nil {
		return err
	}

	return checkSkillAffected(result)
}

func (s *Store) DeleteOwnedSkill(
	ctx context.Context,
	userID string,
	name string,
) error {
	if !skill.ValidName(name) {
		return skill.ErrInvalid
	}

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM user_skills
		WHERE user_id = ? AND name = ?
	`, userID, name)

	if err != nil {
		return err
	}

	return checkSkillAffected(result)
}

func checkSkillAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return sql.ErrNoRows
	}

	return nil
}
