package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/store"
)

func newAuthTestServer(t *testing.T, cfg config.Config) *httptest.Server {
	t.Helper()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "auth-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	testServer := httptest.NewServer(NewRouter(dataStore, cfg))
	t.Cleanup(testServer.Close)
	return testServer
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func TestGoogleLoginRedirects(t *testing.T) {
	testServer := newAuthTestServer(t, config.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "secret",
		BaseURL:            "http://localhost:8080",
	})

	response, err := noRedirectClient().Get(testServer.URL + "/auth/google")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", response.StatusCode)
	}
	location := response.Header.Get("Location")
	if !strings.HasPrefix(location, "https://accounts.google.com/") || !strings.Contains(location, "client-id") {
		t.Fatalf("unexpected redirect target: %s", location)
	}
	foundStateCookie := false
	for _, cookie := range response.Cookies() {
		if cookie.Name == stateCookieName && cookie.Value != "" {
			foundStateCookie = true
			if !strings.Contains(location, "state="+cookie.Value) {
				t.Fatalf("state cookie %q not reflected in redirect URL", cookie.Value)
			}
		}
	}
	if !foundStateCookie {
		t.Fatal("state cookie was not set")
	}
}

func TestGoogleLoginUnconfigured(t *testing.T) {
	testServer := newAuthTestServer(t, config.Config{})

	response, err := http.Get(testServer.URL + "/auth/google")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without credentials, got %d", response.StatusCode)
	}
}

func TestGoogleCallbackStateMismatch(t *testing.T) {
	testServer := newAuthTestServer(t, config.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "secret",
		BaseURL:            "http://localhost:8080",
	})

	request, _ := http.NewRequest(http.MethodGet, testServer.URL+"/auth/google/callback?state=forged&code=x", nil)
	request.AddCookie(&http.Cookie{Name: stateCookieName, Value: "expected"})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on state mismatch, got %d", response.StatusCode)
	}
}

func TestSessionCookieAuthAndLogout(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "session-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	testServer := httptest.NewServer(NewRouter(dataStore, config.Config{}))
	t.Cleanup(testServer.Close)

	user, err := dataStore.FindOrCreateGoogleUser(t.Context(), "sub-session", "session@test.local", "S", "")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sessionID, _, err := dataStore.CreateSession(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	request, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/me", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with session cookie, got %d", response.StatusCode)
	}

	logoutRequest, _ := http.NewRequest(http.MethodPost, testServer.URL+"/auth/logout", nil)
	logoutRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	logoutResponse, err := http.DefaultClient.Do(logoutRequest)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on logout, got %d", logoutResponse.StatusCode)
	}

	retryRequest, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/me", nil)
	retryRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	retryResponse, err := http.DefaultClient.Do(retryRequest)
	if err != nil {
		t.Fatalf("get after logout: %v", err)
	}
	retryResponse.Body.Close()
	if retryResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got %d", retryResponse.StatusCode)
	}
}
