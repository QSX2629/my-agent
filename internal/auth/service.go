package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"my-agent/internal/store"
)

type Service struct {
	store *store.Store
}

func NewService(messageStore *store.Store) *Service {
	return &Service{
		store: messageStore,
	}
}

func (s *Service) Register(
	email string,
	password string,
) error {
	email = strings.TrimSpace(strings.ToLower(email))

	if email == "" || password == "" {
		return errors.New("email and password are required")
	}

	if len(password) < 6 {
		return errors.New("password must contain at least 6 characters")
	}

	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte(password),
		bcrypt.DefaultCost,
	)
	if err != nil {
		return err
	}

	return s.store.CreateUser(
		uuid.New().String(),
		email,
		string(passwordHash),
	)
}

func (s *Service) Login(
	email string,
	password string,
) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))

	user, err := s.store.FindUserByEmail(email)
	if err != nil {
		return "", errors.New("invalid email or password")
	}

	if err := bcrypt.CompareHashAndPassword(
		[]byte(user.PasswordHash),
		[]byte(password),
	); err != nil {
		return "", errors.New("invalid email or password")
	}

	rawToken, err := createToken()
	if err != nil {
		return "", err
	}

	tokenHash := hashToken(rawToken)
	expiresAt := time.Now().
		UTC().
		Add(24 * time.Hour).
		Format(time.RFC3339Nano)

	if err := s.store.SaveToken(
		tokenHash,
		user.ID,
		expiresAt,
	); err != nil {
		return "", err
	}

	return rawToken, nil
}

func (s *Service) Authenticate(
	token string,
) (string, error) {
	return s.store.FindUserIDByToken(hashToken(token))
}

func (s *Service) Logout(token string) error {
	return s.store.DeleteToken(hashToken(token))
}

func createToken() (string, error) {
	buffer := make([]byte, 32)

	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
