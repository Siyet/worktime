package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "api-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })

	testServer := httptest.NewServer(NewRouter(dataStore, config.Config{DevAuth: true}))
	t.Cleanup(testServer.Close)
	return testServer
}

func TestSyncEndpointRoundtrip(t *testing.T) {
	testServer := newTestServer(t)

	projectID := uuid.NewString()
	payload := map[string]any{
		"since": 0,
		"changes": map[string]any{
			"projects": []map[string]any{{
				"id": projectID, "name": "API", "color": "#00ff00",
				"created_at": 100, "updated_at": 100,
			}},
		},
	}
	body, _ := json.Marshal(payload)

	response, err := http.Post(testServer.URL+"/api/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}

	var syncResponse store.SyncResponse
	if err := json.NewDecoder(response.Body).Decode(&syncResponse); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if syncResponse.Seq != 1 || len(syncResponse.Changes.Projects) != 1 {
		t.Fatalf("unexpected response: %+v", syncResponse)
	}
	if syncResponse.Changes.Projects[0].Name != "API" {
		t.Fatalf("unexpected project: %+v", syncResponse.Changes.Projects[0])
	}
}

func TestSyncEndpointRejectsInvalidPayload(t *testing.T) {
	testServer := newTestServer(t)

	body := []byte(`{"changes":{"time_entries":[{"id":"nope","started_at":1,"updated_at":1}]}}`)
	response, err := http.Post(testServer.URL+"/api/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.StatusCode)
	}
}

func TestUnauthorizedWithoutDevAuth(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "noauth-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	testServer := httptest.NewServer(NewRouter(dataStore, config.Config{DevAuth: false}))
	t.Cleanup(testServer.Close)

	response, err := http.Get(testServer.URL + "/api/me")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.StatusCode)
	}
}

func TestAPITokenAuth(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "token-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	testServer := httptest.NewServer(NewRouter(dataStore, config.Config{DevAuth: false}))
	t.Cleanup(testServer.Close)

	user, err := dataStore.FindOrCreateGoogleUser(t.Context(), "sub-token", "token@test.local", "Token User", "", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, plaintext, err := dataStore.CreateAPIToken(t.Context(), user.ID, "test token")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	request, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/me", nil)
	request.Header.Set("Authorization", "Bearer "+plaintext)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with valid token, got %d", response.StatusCode)
	}

	var me store.User
	if err := json.NewDecoder(response.Body).Decode(&me); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if me.Email != "token@test.local" {
		t.Fatalf("unexpected user: %+v", me)
	}

	badRequest, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/me", nil)
	badRequest.Header.Set("Authorization", "Bearer wt_wrong")
	badResponse, err := http.DefaultClient.Do(badRequest)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer badResponse.Body.Close()
	if badResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with invalid token, got %d", badResponse.StatusCode)
	}
}

// A token lives in plain text on developer machines and inside agent hook settings, so
// it is the credential most likely to leak. Letting it mint a replacement for itself,
// or delete the tokens the owner holds, would mean revoking the leaked one restores
// nothing - so credential management takes the browser session.
func TestAPITokenCannotManageTokens(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "token-scope.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	testServer := httptest.NewServer(NewRouter(dataStore, config.Config{DevAuth: false}))
	t.Cleanup(testServer.Close)

	user, err := dataStore.FindOrCreateGoogleUser(t.Context(), "sub-scope", "scope@test.local", "Scope", "", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	existing, plaintext, err := dataStore.CreateAPIToken(t.Context(), user.ID, "hook token")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list", http.MethodGet, "/api/tokens", ""},
		{"create", http.MethodPost, "/api/tokens", `{"name":"second"}`},
		{"delete", http.MethodDelete, "/api/tokens/" + existing.ID, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request, _ := http.NewRequest(testCase.method, testServer.URL+testCase.path, bytes.NewBufferString(testCase.body))
			request.Header.Set("Authorization", "Bearer "+plaintext)
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("expected 403 for a token-authenticated request, got %d", response.StatusCode)
			}
		})
	}

	// The owner's token is still there.
	tokens, err := dataStore.ListAPITokens(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected the original token untouched, got %d", len(tokens))
	}
}

// SameSite=Lax treats a sibling host on the same registrable domain as same-site, so on
// an instance sharing a domain with anything else that page could write into this one's
// data with the victim's cookie attached.
func TestCrossSiteWritesAreRefused(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "csrf.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	testServer := httptest.NewServer(NewRouter(dataStore, config.Config{DevAuth: true}))
	t.Cleanup(testServer.Close)

	post := func(headers map[string]string) int {
		request, _ := http.NewRequest(http.MethodPost, testServer.URL+"/api/sync", bytes.NewBufferString(`{"since":0}`))
		request.Header.Set("Content-Type", "application/json")
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}

	if status := post(map[string]string{"Sec-Fetch-Site": "cross-site"}); status != http.StatusForbidden {
		t.Fatalf("expected a cross-site push refused, got %d", status)
	}
	if status := post(map[string]string{"Origin": "https://evil.example"}); status != http.StatusForbidden {
		t.Fatalf("expected a foreign Origin refused, got %d", status)
	}
	if status := post(map[string]string{"Sec-Fetch-Site": "same-origin"}); status != http.StatusOK {
		t.Fatalf("the app's own push must still work, got %d", status)
	}
	if status := post(nil); status != http.StatusOK {
		t.Fatalf("a request without either header must still work, got %d", status)
	}
	// The app is reached by whatever name the user typed, which is rarely the
	// configured base URL: a bare IP, a LAN name, a proxy, a random port. Comparing
	// the Origin against the configured URL alone would lock the owner out of their
	// own instance while stopping no attacker.
	ownOrigin := strings.TrimSuffix(testServer.URL, "/")
	if status := post(map[string]string{"Origin": ownOrigin}); status != http.StatusOK {
		t.Fatalf("a push from the host the request arrived on must work, got %d", status)
	}
}

func TestOriginValidationIncludesTrustedExternalScheme(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://internal.example/api/sync", nil)
	request.Host = "internal.example"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "worktime.example")

	direct := &server{cfg: config.Config{BaseURL: "https://configured.example"}}
	if !direct.originAllowed("http://internal.example", request) {
		t.Fatal("direct HTTP request rejected its exact scheme and host")
	}
	if direct.originAllowed("https://internal.example", request) {
		t.Fatal("same host with a different scheme was accepted")
	}
	if direct.originAllowed("https://worktime.example", request) {
		t.Fatal("untrusted forwarded origin was accepted")
	}
	if !direct.originAllowed("https://configured.example", request) {
		t.Fatal("configured external scheme and host were rejected")
	}

	proxied := &server{cfg: config.Config{BaseURL: "https://configured.example", TrustProxy: true}}
	if !proxied.originAllowed("https://worktime.example", request) {
		t.Fatal("explicitly trusted proxy scheme and host were rejected")
	}
	if proxied.originAllowed("http://worktime.example", request) {
		t.Fatal("proxy origin with a mismatched scheme was accepted")
	}
	request.Header.Set("X-Forwarded-Proto", "https,http")
	if proxied.originAllowed("https://worktime.example", request) {
		t.Fatal("ambiguous forwarded scheme was accepted")
	}
}

// Signing out is a state change too, and it sits outside /api - so it needs the guard
// wired explicitly rather than inherited from the route group.
func TestCrossSiteLogoutIsRefused(t *testing.T) {
	testServer := newTestServer(t)

	request, _ := http.NewRequest(http.MethodPost, testServer.URL+"/auth/logout", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected a cross-site logout refused, got %d", response.StatusCode)
	}

	same, _ := http.NewRequest(http.MethodPost, testServer.URL+"/auth/logout", nil)
	same.Header.Set("Sec-Fetch-Site", "same-origin")
	sameResponse, err := http.DefaultClient.Do(same)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer sameResponse.Body.Close()
	if sameResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("the app's own logout must still work, got %d", sameResponse.StatusCode)
	}
}
