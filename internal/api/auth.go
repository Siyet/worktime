package api

import (
	"net/http"
	"net/url"
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
				ctx := authctx.WithAPIToken(authctx.WithUser(r.Context(), user))
				next.ServeHTTP(w, r.WithContext(ctx))
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

// requireSession refuses API-token authentication. Token routes are behind it because
// a leaked token would otherwise be able to issue a replacement for itself and delete
// the owner's tokens, so revoking it would restore nothing. Only the browser session -
// which needs the Google sign-in - manages credentials.
func requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authctx.IsAPIToken(r.Context()) {
			http.Error(w, "API tokens cannot manage tokens; sign in to do that", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireSameOrigin blocks cross-site state changes on cookie-authenticated requests.
// The session cookie is SameSite=Lax, which a sibling host on the same registrable
// domain does not count as cross-site - on a self-hosted instance sharing a domain with
// anything else, that is enough for another page to write into this one's data.
//
// API tokens are exempt: they are sent deliberately, never attached by a browser, and
// the hook and MCP clients send neither Origin nor Sec-Fetch-Site.
func (s *server) requireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		safe := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
		if safe || authctx.IsAPIToken(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin, r.Host) {
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed compares the Origin against the host the request actually arrived on,
// not only against the configured base URL. The two differ in every deployment that is
// reached by more than one name - a reverse proxy, a bare IP, a LAN address, the
// random port the end-to-end suite binds - and rejecting those would break the app for
// its own users rather than for an attacker. The configured base URL is accepted as
// well, for a proxy that rewrites Host.
func (s *server) originAllowed(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Host == requestHost {
		return true
	}
	base, err := url.Parse(s.cfg.BaseURL)
	return err == nil && base.Host != "" && parsed.Host == base.Host
}
