package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
)

const sessionTTL = 90 * 24 * time.Hour

// FindOrCreateGoogleUser looks a user up by Google subject, creating the account on
// first sign-in. Email is stored lowercase.
func (s *Store) FindOrCreateGoogleUser(ctx context.Context, googleSub, email, name, pictureURL string) (User, error) {
	user := User{Email: email, Name: name, PictureURL: pictureURL}
	err := s.db.QueryRowContext(ctx,
		"SELECT id, email, name, picture_url FROM users WHERE google_sub = ?", googleSub,
	).Scan(&user.ID, &user.Email, &user.Name, &user.PictureURL)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	user.ID = uuid.NewString()
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO users (id, google_sub, email, name, picture_url, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		user.ID, googleSub, email, name, pictureURL, time.Now().UnixMilli())
	if err != nil {
		return User{}, err
	}
	return user, nil
}

// EnsureDevUser returns the local development user, creating it if needed.
func (s *Store) EnsureDevUser(ctx context.Context) (User, error) {
	return s.FindOrCreateGoogleUser(ctx, "dev", "dev@worktime.local", "Dev User", "")
}

func (s *Store) GetUser(ctx context.Context, userID string) (User, error) {
	var user User
	err := s.db.QueryRowContext(ctx,
		"SELECT id, email, name, picture_url FROM users WHERE id = ?", userID,
	).Scan(&user.ID, &user.Email, &user.Name, &user.PictureURL)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}

// CreateSession issues a new opaque session ID for the user.
func (s *Store) CreateSession(ctx context.Context, userID string) (string, time.Time, error) {
	sessionID := randomToken()
	now := time.Now()
	expiresAt := now.Add(sessionTTL)
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)",
		sessionID, userID, now.UnixMilli(), expiresAt.UnixMilli())
	if err != nil {
		return "", time.Time{}, err
	}
	return sessionID, expiresAt, nil
}

func (s *Store) GetUserBySession(ctx context.Context, sessionID string) (User, error) {
	var user User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.name, u.picture_url
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = ? AND s.expires_at > ?`, sessionID, time.Now().UnixMilli(),
	).Scan(&user.ID, &user.Email, &user.Name, &user.PictureURL)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}

func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", sessionID)
	return err
}

type APIToken struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at"`
}

// CreateAPIToken returns the plaintext token once; only its SHA-256 hash is stored.
func (s *Store) CreateAPIToken(ctx context.Context, userID, name string) (APIToken, string, error) {
	plaintext := "wt_" + randomToken()
	token := APIToken{ID: uuid.NewString(), Name: name, CreatedAt: time.Now().UnixMilli()}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO api_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)",
		token.ID, userID, name, hashToken(plaintext), token.CreatedAt)
	if err != nil {
		return APIToken{}, "", err
	}
	return token, plaintext, nil
}

func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, created_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY created_at", userID)
	if err != nil {
		return nil, err
	}
	tokens := []APIToken{}
	for rows.Next() {
		var token APIToken
		if err := rows.Scan(&token.ID, &token.Name, &token.CreatedAt, &token.LastUsedAt); err != nil {
			rows.Close()
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, closeRows(rows)
}

func (s *Store) DeleteAPIToken(ctx context.Context, userID, tokenID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM api_tokens WHERE id = ? AND user_id = ?", tokenID, userID)
	return err
}

func (s *Store) GetUserByAPIToken(ctx context.Context, plaintext string) (User, error) {
	var user User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.name, u.picture_url
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, hashToken(plaintext),
	).Scan(&user.ID, &user.Email, &user.Name, &user.PictureURL)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	_, err = s.db.ExecContext(ctx,
		"UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?", time.Now().UnixMilli(), hashToken(plaintext))
	return user, err
}

func randomToken() string {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buffer)
}

func hashToken(plaintext string) string {
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:])
}
