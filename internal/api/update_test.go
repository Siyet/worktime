package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/store"
	appupdate "github.com/Siyet/worktime/internal/update"
)

func newUpdateTestHandler(t *testing.T, cfg config.Config) (http.Handler, *store.Store) {
	t.Helper()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "updates.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	return NewRouter(dataStore, cfg), dataStore
}

func TestPersistedUpdateStatusIsDisplayOnlyOverAPI(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := store.Open(filepath.Join(directory, "worktime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	updateDirectory := filepath.Join(directory, "update")
	if err := os.MkdirAll(updateDirectory, 0o700); err != nil {
		t.Fatalf("create update directory: %v", err)
	}
	state := `{"schema_version":1,"generation":4,"version":"v1.2.0","checked_at":1788000000000,"changelog_url":"https://github.com/Siyet/worktime/releases/tag/v1.2.0"}`
	if err := os.WriteFile(filepath.Join(updateDirectory, "highest-seen.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("write persisted update state: %v", err)
	}
	manager := appupdate.NewManager(appupdate.Options{
		CurrentVersion: "v1.0.0", DataDirectory: directory, Policy: dataStore,
	})
	handler := NewRouter(dataStore, config.Config{
		DevAuth: true, AdminEmails: []string{"dev@worktime.local"},
	}, RouterOptions{Updates: manager})

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/system/update", nil))
	var status map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["state"] != "available" || status["update_available"] != true || status["apply_ready"] != false {
		t.Fatalf("persisted API status was not display-only: %+v", status)
	}

	applyResponse := httptest.NewRecorder()
	handler.ServeHTTP(applyResponse, httptest.NewRequest(http.MethodPost, "/api/system/update/apply", nil))
	if applyResponse.Code != http.StatusConflict {
		t.Fatalf("cached update apply status = %d, want 409", applyResponse.Code)
	}
}

func TestUpdateStatusExposesVersionButDefaultsToNoMutationAuthority(t *testing.T) {
	handler, _ := newUpdateTestHandler(t, config.Config{DevAuth: true})

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/system/update", nil))
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status code: %d", statusResponse.Code)
	}
	var status map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status["can_manage"] != false {
		t.Fatalf("default deployment granted mutation authority: %+v", status)
	}

	policyResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/system/update/policy", bytes.NewBufferString("{\"auto_apply\":true}"))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(policyResponse, request)
	if policyResponse.Code != http.StatusForbidden {
		t.Fatalf("policy status = %d, want 403", policyResponse.Code)
	}
}

func TestExplicitAdminCanManagePolicyButCannotEnableNotificationOnlyApply(t *testing.T) {
	handler, _ := newUpdateTestHandler(t, config.Config{
		DevAuth: true, AdminEmails: []string{"dev@worktime.local"},
	})
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/system/update", nil))
	var status map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status["can_manage"] != true {
		t.Fatalf("explicit admin was not recognized: %+v", status)
	}

	policyResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/system/update/policy", bytes.NewBufferString("{\"auto_apply\":true}"))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(policyResponse, request)
	if policyResponse.Code != http.StatusBadRequest {
		t.Fatalf("notification-only auto policy status = %d, want 400", policyResponse.Code)
	}
}

func TestAPITokenCannotMutateUpdatePolicyEvenForAdminEmail(t *testing.T) {
	handler, dataStore := newUpdateTestHandler(t, config.Config{AdminEmails: []string{"owner@example.com"}})
	user, err := dataStore.FindOrCreateGoogleUser(t.Context(), "admin-sub", "owner@example.com", "Owner", "", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, plaintext, err := dataStore.CreateAPIToken(t.Context(), user.ID, "agent")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/system/update/policy", bytes.NewBufferString("{\"auto_apply\":false}"))
	request.Header.Set("Authorization", "Bearer "+plaintext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("API token mutation status = %d, want 403", response.Code)
	}
}
