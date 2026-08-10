// Package authctx carries the authenticated user through request contexts.
// It lives separately so both the HTTP API and the MCP server can use it
// without an import cycle.
package authctx

import (
	"context"

	"github.com/Siyet/worktime/internal/store"
)

type contextKey int

const (
	userKey  contextKey = 0
	tokenKey contextKey = 1
)

func WithUser(ctx context.Context, user store.User) context.Context {
	return context.WithValue(ctx, userKey, user)
}

func User(ctx context.Context) (store.User, bool) {
	user, ok := ctx.Value(userKey).(store.User)
	return user, ok
}

// WithAPIToken marks a request as authenticated by an API token rather than by the
// session cookie. The two are not equivalent: a token is a long-lived credential
// carried by scripts and agent hooks, kept in plain text on developer machines, so it
// must not be able to mint further tokens or revoke the ones the owner holds.
func WithAPIToken(ctx context.Context) context.Context {
	return context.WithValue(ctx, tokenKey, true)
}

func IsAPIToken(ctx context.Context) bool {
	marked, _ := ctx.Value(tokenKey).(bool)
	return marked
}
