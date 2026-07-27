package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const stateCookieName = "wt_oauth_state"

func (s *server) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     s.cfg.GoogleClientID,
		ClientSecret: s.cfg.GoogleClientSecret,
		RedirectURL:  strings.TrimSuffix(s.cfg.BaseURL, "/") + "/auth/google/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

func (s *server) secureCookies() bool {
	return strings.HasPrefix(s.cfg.BaseURL, "https://")
}

// handleGoogleLogin starts the OAuth flow: sets a random state cookie and
// redirects the browser to Google's consent screen.
func (s *server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.GoogleClientID == "" || s.cfg.GoogleClientSecret == "" {
		http.Error(w, "Google sign-in is not configured on this instance "+
			"(set WORKTIME_GOOGLE_CLIENT_ID and WORKTIME_GOOGLE_CLIENT_SECRET)", http.StatusServiceUnavailable)
		return
	}
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(stateBytes)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/auth",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.oauthConfig().AuthCodeURL(state), http.StatusFound)
}

type googleUserinfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func (s *server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" || r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "OAuth state mismatch, please retry sign-in", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	token, err := s.oauthConfig().Exchange(r.Context(), code)
	if err != nil {
		log.Printf("oauth exchange: %v", err)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}

	// The userinfo endpoint is called server-side with the token we just received
	// directly from Google, so no local JWT verification is needed.
	client := s.oauthConfig().Client(r.Context(), token)
	userinfoResponse, err := client.Get("https://openidconnect.googleapis.com/v1/userinfo")
	if err != nil {
		log.Printf("userinfo: %v", err)
		http.Error(w, "failed to fetch user info", http.StatusBadGateway)
		return
	}
	defer userinfoResponse.Body.Close()
	var userinfo googleUserinfo
	if err := json.NewDecoder(userinfoResponse.Body).Decode(&userinfo); err != nil || userinfo.Sub == "" {
		http.Error(w, "invalid user info response", http.StatusBadGateway)
		return
	}

	email := strings.ToLower(userinfo.Email)
	if !userinfo.EmailVerified {
		http.Error(w, "email is not verified with Google", http.StatusForbidden)
		return
	}
	if len(s.cfg.AllowedEmails) > 0 && !slices.Contains(s.cfg.AllowedEmails, email) {
		http.Error(w, "this account is not allowed on this instance", http.StatusForbidden)
		return
	}

	user, err := s.store.FindOrCreateGoogleUser(r.Context(), userinfo.Sub, email, userinfo.Name, userinfo.Picture)
	if err != nil {
		log.Printf("find or create user: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sessionID, expiresAt, err := s.store.CreateSession(r.Context(), user.ID)
	if err != nil {
		log.Printf("create session: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: "", Path: "/auth", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := s.store.DeleteSession(r.Context(), cookie.Value); err != nil {
			log.Printf("delete session: %v", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthConfig lets the frontend know which sign-in methods are available.
func (s *server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"google":   s.cfg.GoogleClientID != "" && s.cfg.GoogleClientSecret != "",
		"dev_auth": s.cfg.DevAuth,
	})
}
