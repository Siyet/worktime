package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
)

const sessionTTL = 90 * 24 * time.Hour

// FindOrCreateGoogleUser looks a user up by Google subject, creating the account on
// first sign-in. Email is stored lowercase.
//
// A row found by email but carrying a different subject is adopted rather than
// rejected. Google subjects are stable in practice but not guaranteed - an account
// deleted and recreated, or moved between a personal and a Workspace tenant on the same
// address, arrives with a new one - and users.email is UNIQUE, so the insert would fail
// and lock the owner out of their own data with no recovery path in the product.
//
// Adopting by email is only safe because the caller has already verified the address:
// google.go requires email_verified on the ID token and filters against
// WORKTIME_ALLOWED_EMAILS. Weakening either of those turns this into account takeover.
func (s *Store) FindOrCreateGoogleUser(ctx context.Context, googleSub, email, name, pictureURL string) (User, error) {
	user := User{Email: email, Name: name, PictureURL: pictureURL}
	now := time.Now().UnixMilli()

	err := s.db.QueryRowContext(ctx,
		"SELECT id FROM users WHERE google_sub = ?", googleSub,
	).Scan(&user.ID)
	if err == nil {
		// The profile is refreshed on every sign-in: scanning the stored row over the
		// fresh values instead froze the name and avatar at whatever they were the
		// first time the account was seen.
		if _, err := s.db.ExecContext(ctx,
			"UPDATE users SET email = ?, name = ?, picture_url = ? WHERE id = ?",
			email, name, pictureURL, user.ID); err != nil {
			return User{}, err
		}
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}

	if err := s.db.QueryRowContext(ctx,
		"SELECT id FROM users WHERE email = ?", email,
	).Scan(&user.ID); err == nil {
		if _, err := s.db.ExecContext(ctx,
			"UPDATE users SET google_sub = ?, name = ?, picture_url = ? WHERE id = ?",
			googleSub, name, pictureURL, user.ID); err != nil {
			return User{}, err
		}
		return user, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}

	user.ID = uuid.NewString()
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO users (id, google_sub, email, name, picture_url, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		user.ID, googleSub, email, name, pictureURL, now); err != nil {
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

// DeleteExpiredSessions drops sign-ins past their TTL. Lookups already filter on
// expires_at, so these rows are inert - but one accumulates per sign-in per device and
// nothing else ever removes them. This is also the only query that ranges over
// expires_at, which is what idx_sessions_expiry was created for.
func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", time.Now().UnixMilli())
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

// lastUsedResolution is how stale last_used_at is allowed to be. The field exists so
// the owner can see which tokens are alive, which a minute's granularity answers; the
// point is to keep an authenticated read from writing to the single connection on every
// agent heartbeat.
const lastUsedResolution = int64(60_000)

func (s *Store) GetUserByAPIToken(ctx context.Context, plaintext string) (User, error) {
	digest := hashToken(plaintext)
	var user User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.name, u.picture_url
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, digest,
	).Scan(&user.ID, &user.Email, &user.Name, &user.PictureURL)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	// The token is already known to be valid, so a failure of this bookkeeping write
	// must not be reported as an authentication failure: the caller turns any error
	// into 401, and the Claude Code hook treats 401 as permanent and drops the signal
	// instead of retrying it. A full disk would silently cost tracked time.
	now := time.Now().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE api_tokens SET last_used_at = ?
		WHERE token_hash = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now, digest, now-lastUsedResolution); err != nil {
		log.Printf("api token last_used_at: %v", err)
	}
	return user, nil
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
