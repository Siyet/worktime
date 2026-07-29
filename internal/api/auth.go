package api

import (
	"net/http"
	"strings"

	"github.com/Siyet/worktime/internal/authctx"
	"github.com/Siyet/worktime/internal/store"
)

const sessionCookieName = "wt_session"

// currentUser returns the authenticated user placed into the request context by requireAuth.
func currentUser(r *http.Request) store.User {
	user, _ := authctx.User(r.Context())
	return user
}

// requireAuth resolves the user from a Bearer API token, then from the session
// cookie, then (in dev mode) falls back to the local dev user.
func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			user, err := s.store.GetUserByAPIToken(r.Context(), strings.TrimPrefix(header, "Bearer "))
			if err == nil {
				next.ServeHTTP(w, r.WithContext(authctx.WithUser(r.Context(), user)))
				return
			}
			http.Error(w, "invalid API token", http.StatusUnauthorized)
			return
		}

		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			user, err := s.store.GetUserBySession(r.Context(), cookie.Value)
			if err == nil {
				next.ServeHTTP(w, r.WithContext(authctx.WithUser(r.Context(), user)))
				return
			}
		}

		if s.cfg.DevAuth {
			user, err := s.store.EnsureDevUser(r.Context())
			if err == nil {
				next.ServeHTTP(w, r.WithContext(authctx.WithUser(r.Context(), user)))
				return
			}
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}
