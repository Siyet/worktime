package mcpserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Siyet/worktime/internal/api"
	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/store"
)

// bearerTransport adds the API token to every MCP HTTP request.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(request)
}

// mcpFixture is a connected MCP client plus the store behind it, so tests can
// set up server-side state (agent sessions) the tools then operate on.
type mcpFixture struct {
	session *mcp.ClientSession
	store   *store.Store
	userID  string
}

func newMCPSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return newMCPFixture(t).session
}

func newMCPFixture(t *testing.T) mcpFixture {
	t.Helper()

	dataStore, err := store.Open(filepath.Join(t.TempDir(), "mcp-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })

	testServer := httptest.NewServer(api.NewRouter(dataStore, config.Config{}))
	t.Cleanup(testServer.Close)

	user, err := dataStore.FindOrCreateGoogleUser(t.Context(), "sub-mcp", "mcp@test.local", "MCP User", "", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, plaintext, err := dataStore.CreateAPIToken(t.Context(), user.ID, "mcp token")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: testServer.URL + "/mcp",
		HTTPClient: &http.Client{
			Transport: bearerTransport{token: plaintext, base: http.DefaultTransport},
		},
	}
	session, err := client.Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return mcpFixture{session: session, store: dataStore, userID: user.ID}
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("tool %s returned error: %+v", name, result.Content)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode structured content of %s: %v", name, err)
	}
	return decoded
}

func TestMCPTimerFlow(t *testing.T) {
	session := newMCPSession(t)

	// Create a project and start two concurrent timers.
	callTool(t, session, "create_project", map[string]any{"name": "Backend", "color": "#ff0000"})
	callTool(t, session, "start_timer", map[string]any{"description": "API work", "project": "Backend"})
	callTool(t, session, "start_timer", map[string]any{"description": "Code review"})

	running := callTool(t, session, "list_running_timers", nil)
	timers, ok := running["timers"].([]any)
	if !ok || len(timers) != 2 {
		t.Fatalf("expected 2 running timers, got %+v", running)
	}

	// Stop one timer by ID.
	firstTimer := timers[0].(map[string]any)
	callTool(t, session, "stop_timer", map[string]any{"entry_id": firstTimer["entry_id"]})

	afterStop := callTool(t, session, "list_running_timers", nil)
	if len(afterStop["timers"].([]any)) != 1 {
		t.Fatalf("expected 1 running timer after stop, got %+v", afterStop)
	}

	// Stop the rest.
	stopped := callTool(t, session, "stop_all_timers", nil)
	if stopped["stopped_count"].(float64) != 1 {
		t.Fatalf("expected stop_all to stop 1 timer, got %+v", stopped)
	}
}

func TestMCPUpdateTimeEntryMovesTheRunningTimer(t *testing.T) {
	session := newMCPSession(t)

	callTool(t, session, "create_project", map[string]any{"name": "Backend"})
	callTool(t, session, "create_project", map[string]any{"name": "Frontend"})
	started := callTool(t, session, "start_timer", map[string]any{"description": "API work", "project": "Backend"})

	// The common case: one timer runs, so the entry does not have to be named.
	moved := callTool(t, session, "update_time_entry", map[string]any{"project": "Frontend"})
	if moved["project"] != "Frontend" || moved["entry_id"] != started["entry_id"] {
		t.Fatalf("expected the running entry on Frontend, got %+v", moved)
	}
	running := callTool(t, session, "list_running_timers", nil)
	timer := running["timers"].([]any)[0].(map[string]any)
	if timer["project"] != "Frontend" || timer["description"] != "API work" {
		t.Fatalf("expected the stored row on Frontend with its description intact, got %+v", timer)
	}

	// An empty project detaches the entry; a description can be edited in the same call.
	detached := callTool(t, session, "update_time_entry", map[string]any{
		"entry_id": started["entry_id"], "project": "", "description": "code review",
	})
	if detached["project"] != nil || detached["description"] != "code review" {
		t.Fatalf("expected a detached entry with the new description, got %+v", detached)
	}
	running = callTool(t, session, "list_running_timers", nil)
	timer = running["timers"].([]any)[0].(map[string]any)
	if timer["project"] != nil || timer["description"] != "code review" {
		t.Fatalf("expected the stored row without a project, got %+v", timer)
	}
}

func TestMCPUpdateTimeEntryRefusesAnAmbiguousTarget(t *testing.T) {
	session := newMCPSession(t)

	callTool(t, session, "create_project", map[string]any{"name": "Backend"})
	callTool(t, session, "start_timer", map[string]any{"description": "first"})
	callTool(t, session, "start_timer", map[string]any{"description": "second"})

	// Concurrent timers are a feature, so "the running one" has no answer here and
	// picking either would move time off the entry the caller meant.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "update_time_entry", Arguments: map[string]any{"project": "Backend"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an error while two timers run, got %+v", result)
	}

	// An unknown project must not be created behind the caller's back either.
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "update_time_entry", Arguments: map[string]any{"project": "Nope"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an error for an unknown project, got %+v", result)
	}
}

// The agent's own row is the one an agent will want to move to a project, and it
// carries server-owned columns a push must never rewrite - a project change that
// dropped paused_ms would inflate the tracked duration by the idle time.
func TestMCPUpdateTimeEntryKeepsAgentBookkeeping(t *testing.T) {
	fixture := newMCPFixture(t)
	policy := store.AgentPolicy{IdleMs: 10 * 60 * 1000, ToolMaxMs: 30 * 60 * 1000, MaxPauseMs: 4 * 60 * 60 * 1000}
	now := time.Now().UnixMilli()

	sessionID := uuid.NewString()
	agentSession, err := fixture.store.StartAgentSession(t.Context(), fixture.userID, store.AgentStart{
		SessionID: sessionID, StartedAt: now - 60*60_000,
	}, policy)
	if err != nil {
		t.Fatalf("start agent session: %v", err)
	}
	if _, err := fixture.store.AgentHeartbeat(t.Context(), fixture.userID, sessionID,
		store.AgentSignal{At: now - 30*60_000}, policy); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	before, err := fixture.store.GetTimeEntry(t.Context(), fixture.userID, *agentSession.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if before.PausedMs == 0 {
		t.Fatalf("expected the idle gap to be recorded, got %+v", before)
	}

	callTool(t, fixture.session, "create_project", map[string]any{"name": "WorkTime"})
	moved := callTool(t, fixture.session, "update_time_entry", map[string]any{"project": "WorkTime"})
	if moved["session_tag"] != store.AgentSessionTag(sessionID) {
		t.Fatalf("expected the answer to name the agent session, got %+v", moved)
	}

	// The session itself has to learn the project, or the entry it opens after the
	// next midnight cut would land under no project again.
	agentSession, err = fixture.store.GetAgentSession(t.Context(), fixture.userID, sessionID)
	if err != nil {
		t.Fatalf("get agent session: %v", err)
	}
	if agentSession.ProjectID == nil {
		t.Fatalf("expected the session to carry the project, got %+v", agentSession)
	}

	after, err := fixture.store.GetTimeEntry(t.Context(), fixture.userID, before.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if after.ProjectID == nil {
		t.Fatalf("expected the agent row to be on a project, got %+v", after)
	}
	if after.PausedMs != before.PausedMs || after.AgentSessionID == nil || *after.AgentSessionID != sessionID {
		t.Fatalf("expected paused_ms and the session link to survive the edit, got %+v", after)
	}
}

func TestMCPTimeOffAndReport(t *testing.T) {
	session := newMCPSession(t)

	callTool(t, session, "add_time_off", map[string]any{
		"kind": "vacation", "date_from": "2026-07-20", "date_to": "2026-07-22",
	})
	timeOff := callTool(t, session, "list_time_off", nil)
	if len(timeOff["time_off"].([]any)) != 1 {
		t.Fatalf("expected 1 time off record, got %+v", timeOff)
	}

	callTool(t, session, "create_project", map[string]any{"name": "Main"})
	callTool(t, session, "add_time_entry", map[string]any{
		"description": "retro work", "project": "Main",
		"started_at": "2026-07-21T09:00:00Z", "stopped_at": "2026-07-21T11:30:00Z",
	})

	report := callTool(t, session, "time_report", map[string]any{"from": "2026-07-20", "to": "2026-07-26"})
	projects := report["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("expected 1 project row in report, got %+v", report)
	}
	row := projects[0].(map[string]any)
	if row["project"] != "Main" || row["hours"].(float64) != 2.5 {
		t.Fatalf("expected Main with 2.5h, got %+v", row)
	}
	offDays := report["time_off"].([]any)
	if len(offDays) != 1 {
		t.Fatalf("expected vacation days in report, got %+v", report)
	}
	vacation := offDays[0].(map[string]any)
	if vacation["kind"] != "vacation" || vacation["days"].(float64) != 3 {
		t.Fatalf("expected 3 vacation days, got %+v", vacation)
	}
}

func TestMCPRejectsWithoutToken(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "mcp-noauth.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	testServer := httptest.NewServer(api.NewRouter(dataStore, config.Config{}))
	t.Cleanup(testServer.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: testServer.URL + "/mcp"}
	if _, err := client.Connect(t.Context(), transport, nil); err == nil {
		t.Fatal("expected connection without token to fail")
	}
}

// The server listens on loopback because a reverse proxy fronts it, and the SDK reads
// that as "a local MCP server" and refuses any Host that is not loopback. Left on, it
// answers every request through the real domain with 403 before authentication is even
// considered - which is how /mcp stayed broken on the deployed instance without a
// single failing test.
func TestMCPAcceptsAProxiedHost(t *testing.T) {
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "mcp-host.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })
	testServer := httptest.NewServer(api.NewRouter(dataStore, config.Config{}))
	t.Cleanup(testServer.Close)

	user, err := dataStore.FindOrCreateGoogleUser(t.Context(), "sub-host", "host@test.local", "Host User", "", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, plaintext, err := dataStore.CreateAPIToken(t.Context(), user.ID, "host token")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	// A token is required, or requireAuth would answer 401 before the MCP handler runs
	// and the Host would never be judged at all.
	request, _ := http.NewRequest(http.MethodPost, testServer.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Host = "wt.example.com"
	request.Header.Set("Authorization", "Bearer "+plaintext)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("a proxied Host must be served, got %d: %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "set_agent_task") {
		t.Fatalf("expected the tool list, got: %s", body)
	}
}

func TestMCPSetAgentTask(t *testing.T) {
	fixture := newMCPFixture(t)
	policy := store.AgentPolicy{IdleMs: 10 * 60 * 1000}
	startedAt := time.Now().UnixMilli() - 60_000

	sessionID := uuid.NewString()
	agentSession, err := fixture.store.StartAgentSession(t.Context(), fixture.userID, store.AgentStart{
		SessionID: sessionID, StartedAt: startedAt, Cwd: "/home/dev/worktime",
	}, policy)
	if err != nil {
		t.Fatalf("start agent session: %v", err)
	}

	// Until the task is known the row carries the session tag, and the agent can
	// see that from list_running_timers.
	running := callTool(t, fixture.session, "list_running_timers", nil)
	timers := running["timers"].([]any)
	if len(timers) != 1 {
		t.Fatalf("expected the agent entry to be running, got %+v", running)
	}
	timer := timers[0].(map[string]any)
	if timer["session_tag"] != store.AgentSessionTag(sessionID) {
		t.Fatalf("expected the session tag on the running timer, got %+v", timer)
	}
	if timer["task_key"] != nil {
		t.Fatalf("expected no task yet, got %+v", timer)
	}

	result := callTool(t, fixture.session, "set_agent_task", map[string]any{
		"task_key": "MT-12345", "task_title": "Slow AMaaS quote creation",
	})
	if result["session_id"] != sessionID || result["renamed_entries"].(float64) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}

	entry, err := fixture.store.GetTimeEntry(t.Context(), fixture.userID, *agentSession.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Description != "MT-12345 Slow AMaaS quote creation" {
		t.Fatalf("unexpected description: %q", entry.Description)
	}

	running = callTool(t, fixture.session, "list_running_timers", nil)
	timer = running["timers"].([]any)[0].(map[string]any)
	if timer["task_key"] != "MT-12345" {
		t.Fatalf("expected the task key on the running timer, got %+v", timer)
	}
}

func TestMCPSetAgentTaskCwdConstrainsSoleSession(t *testing.T) {
	fixture := newMCPFixture(t)
	policy := store.AgentPolicy{IdleMs: 10 * 60 * 1000}
	sessionID := uuid.NewString()
	beforeSession, err := fixture.store.StartAgentSession(t.Context(), fixture.userID, store.AgentStart{
		SessionID: sessionID, StartedAt: time.Now().UnixMilli() - 60_000, Cwd: "/projects/alpha",
	}, policy)
	if err != nil {
		t.Fatalf("start agent session: %v", err)
	}
	beforeEntry, err := fixture.store.GetTimeEntry(t.Context(), fixture.userID, *beforeSession.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}

	result, err := fixture.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "set_agent_task", Arguments: map[string]any{
			"task_key": "B-123", "task_title": "Other project", "cwd": "/projects/beta",
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	encoded, _ := json.Marshal(result)
	if !result.IsError || !strings.Contains(string(encoded), "/projects/beta") || !strings.Contains(string(encoded), sessionID) {
		t.Fatalf("expected a diagnostic cwd constraint error, got %s", encoded)
	}

	afterSession, err := fixture.store.GetAgentSession(t.Context(), fixture.userID, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	afterEntry, err := fixture.store.GetTimeEntry(t.Context(), fixture.userID, *beforeSession.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry after rejection: %v", err)
	}
	if afterSession.TaskKey != beforeSession.TaskKey || afterSession.TaskTitle != beforeSession.TaskTitle ||
		afterEntry.Description != beforeEntry.Description ||
		afterEntry.ServerSeq != beforeEntry.ServerSeq || afterEntry.UpdatedAt != beforeEntry.UpdatedAt {
		t.Fatalf("rejected MCP call mutated state: session before=%+v after=%+v entry before=%+v after=%+v",
			beforeSession, afterSession, beforeEntry, afterEntry)
	}
}

func TestMCPSetAgentTaskSchemaDescribesSelectors(t *testing.T) {
	fixture := newMCPFixture(t)
	tools, err := fixture.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "set_agent_task" {
			continue
		}
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		schema := string(encoded)
		if !strings.Contains(schema, "authoritative agent session id") ||
			!strings.Contains(schema, "must match exactly one active session") {
			t.Fatalf("selector contract is missing from schema: %s", schema)
		}
		return
	}
	t.Fatal("set_agent_task not found")
}

func TestMCPSetAgentTaskWithoutSession(t *testing.T) {
	fixture := newMCPFixture(t)
	result, err := fixture.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "set_agent_task", Arguments: map[string]any{"task_key": "MT-1"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an error without an active session, got %+v", result)
	}
}

func TestMCPElapsedExcludesAgentPause(t *testing.T) {
	fixture := newMCPFixture(t)
	policy := store.AgentPolicy{IdleMs: 10 * 60 * 1000, ToolMaxMs: 30 * 60 * 1000, MaxPauseMs: 4 * 60 * 60 * 1000}
	now := time.Now().UnixMilli()

	// An hour of wall clock with a half hour of idling inside it: the agent asking
	// "how long have I been on this" must be told half an hour.
	sessionID := uuid.NewString()
	if _, err := fixture.store.StartAgentSession(t.Context(), fixture.userID, store.AgentStart{
		SessionID: sessionID, StartedAt: now - 60*60_000,
	}, policy); err != nil {
		t.Fatalf("start agent session: %v", err)
	}
	if _, err := fixture.store.AgentHeartbeat(t.Context(), fixture.userID, sessionID,
		store.AgentSignal{At: now - 30*60_000}, policy); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	running := callTool(t, fixture.session, "list_running_timers", nil)
	timer := running["timers"].([]any)[0].(map[string]any)
	elapsed, _ := timer["elapsed"].(string)
	if !strings.HasPrefix(elapsed, "0:29:") && !strings.HasPrefix(elapsed, "0:30:") {
		t.Fatalf("expected about half an hour of billable time, got %q", elapsed)
	}
}
