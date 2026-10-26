package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"my-agent/internal/model"
)

type Store struct {
	db *sql.DB
}
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}
type User struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    string
}

func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	store := &Store{db: db}

	if err := store.createTables(); err != nil {
		db.Close()
		return nil, err
	}

	if err := store.createPromptTables(); err != nil {
		db.Close()
		return nil, err
	}

	if err := store.createMemoryTables(); err != nil {
		db.Close()
		return nil, err
	}

	// 本次新增：初始化 Skill 表。
	if err := store.createSkillTables(); err != nil {
		db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) createTables() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    expires_at TEXT NOT NULL
);


		CREATE INDEX IF NOT EXISTS idx_messages_session
		ON messages(session_id, id);
        CREATE TABLE IF NOT EXISTS sessions (
         id TEXT PRIMARY KEY,
         title TEXT NOT NULL,
        created_at TEXT NOT NULL,
         updated_at TEXT NOT NULL
);
	`)

	return err
}

func (s *Store) SaveMessage(
	sessionID string,
	message model.Message,
) error {
	_, err := s.db.Exec(`
		INSERT INTO messages (
			session_id,
			role,
			content,
			created_at
		)
		VALUES (?, ?, ?, ?)
	`,
		sessionID,
		message.Role,
		message.Content,
		time.Now().UTC().Format(time.RFC3339Nano),
	)

	return err
}

func (s *Store) LoadMessages(
	sessionID string,
) ([]model.Message, error) {
	rows, err := s.db.Query(`
		SELECT role, content
		FROM messages
		WHERE session_id = ?
		ORDER BY id ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []model.Message

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

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return messages, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
func (s *Store) CreateSession(
	id string,
	title string,
) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := s.db.Exec(`
		INSERT INTO sessions (
			id,
			title,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?)
	`,
		id,
		title,
		now,
		now,
	)

	return err
}
func (s *Store) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(`
		SELECT id, title, created_at, updated_at
		FROM sessions
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session

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
func (s *Store) DeleteSession(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM messages WHERE session_id = ?`,
		id,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(
		`DELETE FROM sessions WHERE id = ?`,
		id,
	); err != nil {
		return err
	}

	return tx.Commit()
}
func (s *Store) CreateUser(
	id string,
	email string,
	passwordHash string,
) error {
	_, err := s.db.Exec(`
		INSERT INTO users (
			id,
			email,
			password_hash,
			created_at
		)
		VALUES (?, ?, ?, ?)
	`,
		id,
		email,
		passwordHash,
		time.Now().UTC().Format(time.RFC3339Nano),
	)

	return err
}
func (s *Store) FindUserByEmail(
	email string,
) (User, error) {
	var user User

	err := s.db.QueryRow(`
		SELECT id, email, password_hash, created_at
		FROM users
		WHERE email = ?
	`,
		email,
	).Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.CreatedAt,
	)

	return user, err
}
func (s *Store) SaveToken(
	tokenHash string,
	userID string,
	expiresAt string,
) error {
	_, err := s.db.Exec(`
		INSERT INTO auth_tokens (
			token_hash,
			user_id,
			expires_at
		)
		VALUES (?, ?, ?)
	`,
		tokenHash,
		userID,
		expiresAt,
	)

	return err
}

func (s *Store) FindUserIDByToken(
	tokenHash string,
) (string, error) {
	var userID string
	var expiresAt string

	err := s.db.QueryRow(`
		SELECT user_id, expires_at
		FROM auth_tokens
		WHERE token_hash = ?
	`,
		tokenHash,
	).Scan(&userID, &expiresAt)

	if err != nil {
		return "", err
	}

	expireTime, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return "", err
	}

	if time.Now().UTC().After(expireTime) {
		return "", fmt.Errorf("token expired")
	}

	return userID, nil
}

func (s *Store) DeleteToken(tokenHash string) error {
	_, err := s.db.Exec(`
		DELETE FROM auth_tokens
		WHERE token_hash = ?
	`,
		tokenHash,
	)

	return err
}
