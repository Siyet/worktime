package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/store"
)

func newAgentTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "agent-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })

	testServer := httptest.NewServer(NewRouter(dataStore, config.Config{
		DevAuth: true, AgentIdle: 10 * time.Minute, AgentGrace: 10 * time.Minute,
	}))
	t.Cleanup(testServer.Close)
	return testServer
}

func postAgentJSON(t *testing.T, url string, payload map[string]any) (*http.Response, store.AgentSession) {
	t.Helper()
	body, _ := json.Marshal(payload)
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	t.Cleanup(func() { response.Body.Close() })
	var session store.AgentSession
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return response, session
}

func TestAgentSessionEndpointsFlow(t *testing.T) {
	testServer := newAgentTestServer(t)
	sessionID := uuid.NewString()
	base := testServer.URL + "/api/agent/sessions/" + sessionID

	startedAt := int64(1_700_000_000_000)
	response, session := postAgentJSON(t, base+"/start", map[string]any{
		"started_at": startedAt, "source": "claude-code", "cwd": "/home/dev/worktime", "git_branch": "main",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("start: expected 200, got %d", response.StatusCode)
	}
	if session.Status != "active" || session.TimeEntryID == nil {
		t.Fatalf("unexpected session after start: %+v", session)
	}

	response, session = postAgentJSON(t, base+"/heartbeat", map[string]any{"at": startedAt + 60_000})
	if response.StatusCode != http.StatusOK || session.LastHeartbeatAt != startedAt+60_000 {
		t.Fatalf("heartbeat: status %d, session %+v", response.StatusCode, session)
	}

	response, session = postAgentJSON(t, base+"/stop", map[string]any{
		"ended_at": startedAt + 120_000, "reason": "prompt_input_exit",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stop: expected 200, got %d", response.StatusCode)
	}
	if session.Status != "closed" || session.EndedAt == nil || *session.EndedAt != startedAt+120_000 {
		t.Fatalf("unexpected session after stop: %+v", session)
	}
}

func TestAgentSessionEndpointErrors(t *testing.T) {
	testServer := newAgentTestServer(t)

	response, _ := postAgentJSON(t, testServer.URL+"/api/agent/sessions/not-a-uuid/start", map[string]any{})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-UUID session id, got %d", response.StatusCode)
	}

	response, _ = postAgentJSON(t, testServer.URL+"/api/agent/sessions/"+uuid.NewString()+"/stop", map[string]any{})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 stopping an unknown session, got %d", response.StatusCode)
	}
}
