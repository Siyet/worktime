package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/store"
)

func newAgentTestServer(t *testing.T) *httptest.Server {
	testServer, _ := newAgentTestServerAndStore(t)
	return testServer
}

func newAgentTestServerAndStore(t *testing.T) (*httptest.Server, *store.Store) {
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
	return testServer, dataStore
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
	// The entry is opened by the first activity signal, not by the start: a
	// launch that never works must not leave a row.
	if session.Status != "active" || session.TimeEntryID != nil {
		t.Fatalf("unexpected session after start: %+v", session)
	}

	response, session = postAgentJSON(t, base+"/heartbeat", map[string]any{"at": startedAt + 60_000})
	if session.TimeEntryID == nil {
		t.Fatalf("the first heartbeat must open the entry: %+v", session)
	}
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

func TestAgentHeartbeatAcceptsOptionalMetadata(t *testing.T) {
	testServer := newAgentTestServer(t)
	sessionID := uuid.NewString()
	base := testServer.URL + "/api/agent/sessions/" + sessionID
	startedAt := int64(1_700_000_000_000)

	// A session first seen from a heartbeat (the start was lost) still learns its
	// working directory and branch.
	response, session := postAgentJSON(t, base+"/heartbeat", map[string]any{
		"at": startedAt, "activity": "prompt", "cwd": "/home/dev/worktime", "git_branch": "main",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat: expected 200, got %d", response.StatusCode)
	}
	if session.Cwd != "/home/dev/worktime" || session.GitBranch != "main" {
		t.Fatalf("metadata was not stored: %+v", session)
	}

	// A hook shipped before those fields existed sends only "at" and keeps working.
	response, session = postAgentJSON(t, base+"/heartbeat", map[string]any{"at": startedAt + 60_000})
	if response.StatusCode != http.StatusOK || session.LastHeartbeatAt != startedAt+60_000 {
		t.Fatalf("bare heartbeat: status %d, session %+v", response.StatusCode, session)
	}
	if session.Cwd != "/home/dev/worktime" {
		t.Fatalf("a bare heartbeat must not clear metadata: %+v", session)
	}
}

func TestAgentStatusLineReadsRunningTimerWithoutMutatingIt(t *testing.T) {
	testServer, dataStore := newAgentTestServerAndStore(t)
	sessionID := uuid.NewString()
	base := testServer.URL + "/api/agent/sessions/" + sessionID
	startedAt := time.Now().Add(-90 * time.Second).UnixMilli()

	response, started := postAgentJSON(t, base+"/start", map[string]any{
		"started_at": startedAt, "source": "claude-code", "cwd": "/home/dev/worktime",
	})
	if response.StatusCode != http.StatusOK || started.TimeEntryID != nil {
		t.Fatalf("start: status %d, session %+v", response.StatusCode, started)
	}
	// The status line reads a running entry, which activity has to open first.
	if _, started = postAgentJSON(t, base+"/heartbeat", map[string]any{"at": startedAt}); started.TimeEntryID == nil {
		t.Fatalf("heartbeat did not open the entry: %+v", started)
	}
	devUser, err := dataStore.EnsureDevUser(t.Context())
	if err != nil {
		t.Fatalf("get dev user: %v", err)
	}
	beforeSession, err := dataStore.GetAgentSession(t.Context(), devUser.ID, sessionID)
	if err != nil {
		t.Fatalf("get session before status: %v", err)
	}
	beforeEntry, err := dataStore.GetTimeEntry(t.Context(), devUser.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry before status: %v", err)
	}

	statusResponse, err := http.Get(base + "/status-line")
	if err != nil {
		t.Fatalf("GET status line: %v", err)
	}
	defer statusResponse.Body.Close()
	statusBody, err := io.ReadAll(statusResponse.Body)
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("status line: expected 200, got %d: %s", statusResponse.StatusCode, statusBody)
	}
	if !strings.HasPrefix(string(statusBody), "WorkTime 0:") || !strings.Contains(string(statusBody), "Claude Code #") {
		t.Fatalf("unexpected status line %q", statusBody)
	}

	afterSession, err := dataStore.GetAgentSession(t.Context(), devUser.ID, sessionID)
	if err != nil {
		t.Fatalf("get session after status: %v", err)
	}
	afterEntry, err := dataStore.GetTimeEntry(t.Context(), devUser.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry after status: %v", err)
	}
	if !reflect.DeepEqual(beforeSession, afterSession) || !reflect.DeepEqual(beforeEntry, afterEntry) {
		t.Fatalf("reading status mutated tracking state\nsession: %#v -> %#v\nentry: %#v -> %#v",
			beforeSession, afterSession, beforeEntry, afterEntry)
	}

	response, _ = postAgentJSON(t, base+"/stop", map[string]any{
		"ended_at": time.Now().UnixMilli(), "reason": "prompt_input_exit",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stop: expected 200, got %d", response.StatusCode)
	}
	statusResponse, err = http.Get(base + "/status-line")
	if err != nil {
		t.Fatalf("GET closed status line: %v", err)
	}
	defer statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("closed status line: expected 204, got %d", statusResponse.StatusCode)
	}
}

func TestFormatAgentStatusDescriptionStaysOnOneTerminalLine(t *testing.T) {
	if got := formatAgentStatusDescription(" task\n\x1b[31m  title\t"); got != "task [31m title" {
		t.Fatalf("control characters were not removed: %q", got)
	}
	if got := formatAgentStatusDescription(""); got != "untitled" {
		t.Fatalf("empty description: got %q", got)
	}
	long := strings.Repeat("я", 100)
	got := []rune(formatAgentStatusDescription(long))
	if len(got) != 80 || got[79] != '…' {
		t.Fatalf("long description was not rune-safely truncated: %q", string(got))
	}
}

// The setup prompt tells an agent to install the hook straight from the instance
// it will report to. If that ever stops matching the file in the repository, a
// fork ships one protocol and speaks another.
func TestAgentHookAssetsAreServedFromTheBinary(t *testing.T) {
	testServer := newTestServer(t)

	script := getBody(t, testServer.URL+"/api/agent/hook.sh")
	onDisk, err := os.ReadFile(filepath.Join("..", "..", "integrations", "claude-code", "wt-hook.sh"))
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if script != string(onDisk) {
		t.Fatal("served hook script differs from integrations/claude-code/wt-hook.sh")
	}
	if !strings.Contains(script, "tool_start") {
		t.Fatal("served hook script has no tool_start signal")
	}

	statusLine := getBody(t, testServer.URL+"/api/agent/statusline.sh")
	statusLineOnDisk, err := os.ReadFile(filepath.Join("..", "..", "integrations", "claude-code", "wt-statusline.sh"))
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if statusLine != string(statusLineOnDisk) {
		t.Fatal("served status line differs from integrations/claude-code/wt-statusline.sh")
	}
	if !strings.Contains(statusLine, "/status-line") {
		t.Fatal("served status line script does not read the status endpoint")
	}

	settings := getBody(t, testServer.URL+"/api/agent/hook-settings.json")
	var parsed struct {
		Hooks      map[string]json.RawMessage `json:"hooks"`
		StatusLine struct {
			Command         string `json:"command"`
			RefreshInterval int    `json:"refreshInterval"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		t.Fatalf("hook settings are not JSON: %v", err)
	}
	for _, event := range []string{"SessionStart", "PreToolUse", "SessionEnd"} {
		if _, ok := parsed.Hooks[event]; !ok {
			t.Fatalf("hook settings are missing %s", event)
		}
	}
	if !strings.Contains(parsed.StatusLine.Command, "wt-statusline.sh") || parsed.StatusLine.RefreshInterval <= 0 {
		t.Fatalf("hook settings have no refreshing WorkTime status line: %+v", parsed.StatusLine)
	}
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}
