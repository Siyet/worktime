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

func (s *server) isAdmin(r *http.Request) bool {
	if authctx.IsAPIToken(r.Context()) {
		return false
	}
	email := strings.ToLower(currentUser(r).Email)
	for _, allowed := range s.cfg.AdminEmails {
		if email == allowed {
			return true
		}
	}
	return false
}

func (s *server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdmin(r) {
			http.Error(w, "administrator access required", http.StatusForbidden)
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
		if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin, r) {
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed compares both scheme and host. Proxy headers are security inputs only
// when the operator explicitly trusts the immediate proxy; otherwise a direct client
// could claim an HTTPS external origin while sending plain HTTP to WorkTime.
func (s *server) originAllowed(origin string, r *http.Request) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if base, err := url.Parse(s.cfg.BaseURL); err == nil && sameSchemeHost(parsed.Scheme, parsed.Host, base.Scheme, base.Host) {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if s.cfg.TrustProxy {
		forwardedScheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
		forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
		if strings.Contains(forwardedScheme, ",") || strings.Contains(forwardedHost, ",") ||
			(forwardedScheme != "http" && forwardedScheme != "https") || forwardedHost == "" {
			return false
		}
		scheme, host = forwardedScheme, forwardedHost
	}
	return sameSchemeHost(parsed.Scheme, parsed.Host, scheme, host)
}

func sameSchemeHost(leftScheme, leftHost, rightScheme, rightHost string) bool {
	return strings.EqualFold(leftScheme, rightScheme) && strings.EqualFold(leftHost, rightHost)
}
