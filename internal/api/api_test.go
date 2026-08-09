package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
