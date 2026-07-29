package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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

func newMCPSession(t *testing.T) *mcp.ClientSession {
	t.Helper()

	dataStore, err := store.Open(filepath.Join(t.TempDir(), "mcp-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { dataStore.Close() })

	testServer := httptest.NewServer(api.NewRouter(dataStore, config.Config{}))
	t.Cleanup(testServer.Close)

	user, err := dataStore.FindOrCreateGoogleUser(t.Context(), "sub-mcp", "mcp@test.local", "MCP User", "")
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
	return session
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
