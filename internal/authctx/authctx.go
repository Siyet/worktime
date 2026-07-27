// Package authctx carries the authenticated user through request contexts.
// It lives separately so both the HTTP API and the MCP server can use it
// without an import cycle.
package authctx

import (
	"context"

	"worktime/internal/store"
)

type contextKey int

const userKey contextKey = 0

func WithUser(ctx context.Context, user store.User) context.Context {
	return context.WithValue(ctx, userKey, user)
}

func User(ctx context.Context) (store.User, bool) {
	user, ok := ctx.Value(userKey).(store.User)
	return user, ok
}
