// Package mcpserver exposes WorkTime as an MCP server over streamable HTTP.
//
// The handler runs stateless: every request is authenticated by the API
// middleware (Bearer token), and a per-user MCP server instance is built from
// the request context. Tools operate directly on the store and reuse the sync
// machinery so changes propagate to clients through the normal pull path.
package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Siyet/worktime/internal/authctx"
	"github.com/Siyet/worktime/internal/store"
)

const serverVersion = "0.1.0"

func NewHandler(dataStore *store.Store) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		user, ok := authctx.User(r.Context())
		if !ok {
			return nil
		}
		return newServerForUser(dataStore, user)
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}

type toolDeps struct {
	store  *store.Store
	userID string
}

func newServerForUser(dataStore *store.Store, user store.User) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "worktime", Version: serverVersion}, nil)
	deps := toolDeps{store: dataStore, userID: user.ID}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_projects",
		Description: "List all projects with their IDs, colors and archived state.",
	}, deps.listProjects)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_project",
		Description: "Create a new project.",
	}, deps.createProject)
	mcp.AddTool(server, &mcp.Tool{
		Name: "start_timer",
		Description: "Start a new running timer. Multiple timers can run at the same time. " +
			"Use project name (not ID) to attach the timer to a project.",
	}, deps.startTimer)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "stop_timer",
		Description: "Stop a running timer by its entry ID (see list_running_timers).",
	}, deps.stopTimer)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "stop_all_timers",
		Description: "Stop all currently running timers.",
	}, deps.stopAllTimers)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_running_timers",
		Description: "List currently running timers with elapsed time.",
	}, deps.listRunningTimers)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_time_entry",
		Description: "Add a finished time entry retroactively (e.g. for work that was not tracked live).",
	}, deps.addTimeEntry)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_time_off",
		Description: "Record sick leave, vacation or a day off as an inclusive date range. Does not block time tracking.",
	}, deps.addTimeOff)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_time_off",
		Description: "List recorded sick leaves, vacations and days off.",
	}, deps.listTimeOff)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "time_report",
		Description: "Summarize tracked time by project and time-off days for a date range (inclusive).",
	}, deps.timeReport)
	mcp.AddTool(server, &mcp.Tool{
		Name: "set_agent_task",
		Description: "Attach the running agent session to a tracker task and rename every time entry it produced. " +
			"Call this as soon as the task number is known (look the title up in the tracker yourself): until then the " +
			"work is tracked under a technical session tag like 'Claude Code #ab12cd34'.",
	}, deps.setAgentTask)

	return server
}

// --- shared helpers ---

func (d toolDeps) pushEntries(ctx context.Context, entries ...store.TimeEntry) error {
	_, err := d.store.Sync(ctx, d.userID, store.SyncRequest{Changes: store.SyncChanges{TimeEntries: entries}})
	return err
}

// stampUpdate prepares an edit of an existing row for the sync path. Conflicts resolve
// last-write-wins on updated_at, and the stored value carries the *browser* clock while
// this process carries the server's. A browser running a minute ahead would therefore
// make the write lose silently - Sync reports no per-row outcome, so the tool would
// answer "stopped" for a timer that is still running. Stepping past the stored value
// keeps the intent of last-write-wins (this edit is the latest one) without trusting
// either clock to be right.
func stampUpdate(entry *store.TimeEntry, now int64) int64 {
	stamp := now
	if entry.UpdatedAt >= stamp {
		stamp = entry.UpdatedAt + 1
	}
	entry.UpdatedAt = stamp
	return stamp
}

func (d toolDeps) resolveProject(ctx context.Context, name string) (*store.Project, error) {
	if name == "" {
		return nil, nil
	}
	projects, err := d.store.ListProjects(ctx, d.userID)
	if err != nil {
		return nil, err
	}
	for index := range projects {
		if strings.EqualFold(projects[index].Name, name) {
			return &projects[index], nil
		}
	}
	return nil, fmt.Errorf("project %q not found; use list_projects or create_project first", name)
}

func (d toolDeps) projectNames(ctx context.Context) (map[string]string, error) {
	projects, err := d.store.ListProjects(ctx, d.userID)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(projects))
	for _, project := range projects {
		names[project.ID] = project.Name
	}
	return names, nil
}

// billableMs is what an entry is worth: the interval minus the idle time an
// agent session recorded inside it. Every place that shows a duration goes
// through it, or the numbers here and in the UI would drift apart.
func billableMs(entry store.TimeEntry, now int64) int64 {
	end := now
	if entry.StoppedAt != nil {
		end = *entry.StoppedAt
	}
	return max(0, end-entry.StartedAt-entry.PausedMs)
}

func formatDuration(ms int64) string {
	duration := time.Duration(ms) * time.Millisecond
	return fmt.Sprintf("%d:%02d:%02d", int(duration.Hours()), int(duration.Minutes())%60, int(duration.Seconds())%60)
}

// --- tool payloads and handlers ---

type projectOut struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	Archived bool   `json:"archived"`
}

type listProjectsOut struct {
	Projects []projectOut `json:"projects"`
}

func (d toolDeps) listProjects(ctx context.Context, req *mcp.CallToolRequest, input any) (*mcp.CallToolResult, listProjectsOut, error) {
	projects, err := d.store.ListProjects(ctx, d.userID)
	if err != nil {
		return nil, listProjectsOut{}, err
	}
	out := listProjectsOut{Projects: []projectOut{}}
	for _, project := range projects {
		out.Projects = append(out.Projects, projectOut{
			ID: project.ID, Name: project.Name, Color: project.Color, Archived: project.Archived,
		})
	}
	return nil, out, nil
}

type createProjectIn struct {
	Name  string `json:"name" jsonschema:"project name"`
	Color string `json:"color,omitempty" jsonschema:"hex color like #2563eb, optional"`
}

func (d toolDeps) createProject(ctx context.Context, req *mcp.CallToolRequest, input createProjectIn) (*mcp.CallToolResult, projectOut, error) {
	if input.Name == "" {
		return nil, projectOut{}, fmt.Errorf("name is required")
	}
	if existing, _ := d.resolveProject(ctx, input.Name); existing != nil {
		return nil, projectOut{}, fmt.Errorf("project %q already exists", input.Name)
	}
	color := input.Color
	if color == "" {
		color = "#2563eb"
	}
	now := time.Now().UnixMilli()
	project := store.Project{ID: newID(), Name: input.Name, Color: color, CreatedAt: now, UpdatedAt: now}
	_, err := d.store.Sync(ctx, d.userID, store.SyncRequest{Changes: store.SyncChanges{Projects: []store.Project{project}}})
	if err != nil {
		return nil, projectOut{}, err
	}
	return nil, projectOut{ID: project.ID, Name: project.Name, Color: project.Color}, nil
}

type startTimerIn struct {
	Description string `json:"description,omitempty" jsonschema:"what is being worked on"`
	Project     string `json:"project,omitempty" jsonschema:"project name, optional"`
}

type timerOut struct {
	EntryID     string `json:"entry_id"`
	Description string `json:"description"`
	Project     string `json:"project,omitempty"`
	StartedAt   string `json:"started_at"`
	Elapsed     string `json:"elapsed,omitempty"`
	// Agent rows only: the session that owns the row and the task it is attached
	// to, so an agent can see whether set_agent_task still has to be called.
	SessionTag string `json:"session_tag,omitempty"`
	TaskKey    string `json:"task_key,omitempty"`
}

func (d toolDeps) startTimer(ctx context.Context, req *mcp.CallToolRequest, input startTimerIn) (*mcp.CallToolResult, timerOut, error) {
	project, err := d.resolveProject(ctx, input.Project)
	if err != nil {
		return nil, timerOut{}, err
	}
	now := time.Now().UnixMilli()
	entry := store.TimeEntry{ID: newID(), Description: input.Description, StartedAt: now, CreatedAt: now, UpdatedAt: now}
	projectName := ""
	if project != nil {
		entry.ProjectID = &project.ID
		projectName = project.Name
	}
	if err := d.pushEntries(ctx, entry); err != nil {
		return nil, timerOut{}, err
	}
	return nil, timerOut{
		EntryID: entry.ID, Description: entry.Description, Project: projectName,
		StartedAt: time.UnixMilli(now).Format(time.RFC3339),
	}, nil
}

type stopTimerIn struct {
	EntryID string `json:"entry_id" jsonschema:"ID of the running entry to stop"`
}

func (d toolDeps) stopTimer(ctx context.Context, req *mcp.CallToolRequest, input stopTimerIn) (*mcp.CallToolResult, timerOut, error) {
	entry, err := d.store.GetTimeEntry(ctx, d.userID, input.EntryID)
	if err != nil {
		return nil, timerOut{}, fmt.Errorf("entry not found: %s", input.EntryID)
	}
	if entry.StoppedAt != nil {
		return nil, timerOut{}, fmt.Errorf("entry %s is not running", input.EntryID)
	}
	now := time.Now().UnixMilli()
	entry.StoppedAt = &now
	stampUpdate(&entry, now)
	if err := d.pushEntries(ctx, entry); err != nil {
		return nil, timerOut{}, err
	}
	// Sync reports no per-row outcome, so the only honest way to answer "stopped" is
	// to read the row back. Reporting success for a timer that is still running is
	// worse than an error: nobody goes looking for it.
	stored, err := d.store.GetTimeEntry(ctx, d.userID, entry.ID)
	if err != nil {
		return nil, timerOut{}, err
	}
	if stored.StoppedAt == nil {
		return nil, timerOut{}, fmt.Errorf("entry %s is still running: the stop was refused by a newer version of the row",
			entry.ID)
	}
	return nil, timerOut{
		EntryID: entry.ID, Description: entry.Description,
		StartedAt: time.UnixMilli(entry.StartedAt).Format(time.RFC3339),
		Elapsed:   formatDuration(billableMs(stored, *stored.StoppedAt)),
	}, nil
}

type stopAllOut struct {
	StoppedCount int `json:"stopped_count"`
}

func (d toolDeps) stopAllTimers(ctx context.Context, req *mcp.CallToolRequest, input any) (*mcp.CallToolResult, stopAllOut, error) {
	running, err := d.store.ListRunningEntries(ctx, d.userID)
	if err != nil {
		return nil, stopAllOut{}, err
	}
	now := time.Now().UnixMilli()
	updated := make([]store.TimeEntry, 0, len(running))
	for _, entry := range running {
		stoppedAt := now
		entry.StoppedAt = &stoppedAt
		stampUpdate(&entry, now)
		updated = append(updated, entry)
	}
	if len(updated) > 0 {
		if err := d.pushEntries(ctx, updated...); err != nil {
			return nil, stopAllOut{}, err
		}
	}
	// The count reports what actually stopped, not what was attempted.
	stopped := 0
	for _, entry := range updated {
		stored, err := d.store.GetTimeEntry(ctx, d.userID, entry.ID)
		if err != nil {
			return nil, stopAllOut{}, err
		}
		if stored.StoppedAt != nil {
			stopped++
		}
	}
	return nil, stopAllOut{StoppedCount: stopped}, nil
}

type listTimersOut struct {
	Timers []timerOut `json:"timers"`
}

func (d toolDeps) listRunningTimers(ctx context.Context, req *mcp.CallToolRequest, input any) (*mcp.CallToolResult, listTimersOut, error) {
	running, err := d.store.ListRunningEntries(ctx, d.userID)
	if err != nil {
		return nil, listTimersOut{}, err
	}
	names, err := d.projectNames(ctx)
	if err != nil {
		return nil, listTimersOut{}, err
	}
	now := time.Now().UnixMilli()
	out := listTimersOut{Timers: []timerOut{}}
	for _, entry := range running {
		projectName := ""
		if entry.ProjectID != nil {
			projectName = names[*entry.ProjectID]
		}
		timer := timerOut{
			EntryID: entry.ID, Description: entry.Description, Project: projectName,
			StartedAt: time.UnixMilli(entry.StartedAt).Format(time.RFC3339),
			Elapsed:   formatDuration(billableMs(entry, now)),
		}
		if entry.AgentSessionID != nil {
			timer.SessionTag = store.AgentSessionTag(*entry.AgentSessionID)
			if session, err := d.store.GetAgentSession(ctx, d.userID, *entry.AgentSessionID); err == nil {
				timer.TaskKey = session.TaskKey
			}
		}
		out.Timers = append(out.Timers, timer)
	}
	return nil, out, nil
}

type setAgentTaskIn struct {
	TaskKey   string `json:"task_key" jsonschema:"tracker task key, e.g. MT-12345"`
	TaskTitle string `json:"task_title,omitempty" jsonschema:"short task title, optional"`
	SessionID string `json:"session_id,omitempty" jsonschema:"agent session id; omit when a single session is running"`
	Cwd       string `json:"cwd,omitempty" jsonschema:"working directory, picks the session when several are running"`
}

type setAgentTaskOut struct {
	SessionID      string `json:"session_id"`
	TaskKey        string `json:"task_key"`
	TaskTitle      string `json:"task_title,omitempty"`
	RenamedEntries int    `json:"renamed_entries"`
}

func (d toolDeps) setAgentTask(ctx context.Context, req *mcp.CallToolRequest, input setAgentTaskIn) (*mcp.CallToolResult, setAgentTaskOut, error) {
	result, err := d.store.SetAgentTask(ctx, d.userID,
		store.AgentTaskSelector{SessionID: input.SessionID, Cwd: input.Cwd}, input.TaskKey, input.TaskTitle)
	if err != nil {
		return nil, setAgentTaskOut{}, err
	}
	return nil, setAgentTaskOut{
		SessionID: result.Session.ID, TaskKey: result.Session.TaskKey, TaskTitle: result.Session.TaskTitle,
		RenamedEntries: result.RenamedEntries,
	}, nil
}

type addTimeEntryIn struct {
	Description string `json:"description,omitempty" jsonschema:"what the time was spent on"`
	Project     string `json:"project,omitempty" jsonschema:"project name, optional"`
	StartedAt   string `json:"started_at" jsonschema:"start time, RFC3339 like 2026-07-24T09:00:00+03:00"`
	StoppedAt   string `json:"stopped_at" jsonschema:"end time, RFC3339"`
}

func (d toolDeps) addTimeEntry(ctx context.Context, req *mcp.CallToolRequest, input addTimeEntryIn) (*mcp.CallToolResult, timerOut, error) {
	startedAt, err := time.Parse(time.RFC3339, input.StartedAt)
	if err != nil {
		return nil, timerOut{}, fmt.Errorf("started_at must be RFC3339: %w", err)
	}
	stoppedAt, err := time.Parse(time.RFC3339, input.StoppedAt)
	if err != nil {
		return nil, timerOut{}, fmt.Errorf("stopped_at must be RFC3339: %w", err)
	}
	if stoppedAt.Before(startedAt) {
		return nil, timerOut{}, fmt.Errorf("stopped_at is before started_at")
	}
	project, err := d.resolveProject(ctx, input.Project)
	if err != nil {
		return nil, timerOut{}, err
	}
	now := time.Now().UnixMilli()
	stoppedMs := stoppedAt.UnixMilli()
	entry := store.TimeEntry{
		ID: newID(), Description: input.Description,
		StartedAt: startedAt.UnixMilli(), StoppedAt: &stoppedMs,
		CreatedAt: now, UpdatedAt: now,
	}
	projectName := ""
	if project != nil {
		entry.ProjectID = &project.ID
		projectName = project.Name
	}
	if err := d.pushEntries(ctx, entry); err != nil {
		return nil, timerOut{}, err
	}
	return nil, timerOut{
		EntryID: entry.ID, Description: entry.Description, Project: projectName,
		StartedAt: startedAt.Format(time.RFC3339),
		Elapsed:   formatDuration(stoppedMs - entry.StartedAt),
	}, nil
}

type addTimeOffIn struct {
	Kind     string `json:"kind" jsonschema:"sick, vacation or dayoff"`
	DateFrom string `json:"date_from" jsonschema:"first day, YYYY-MM-DD"`
	DateTo   string `json:"date_to" jsonschema:"last day inclusive, YYYY-MM-DD"`
	Note     string `json:"note,omitempty" jsonschema:"optional note"`
}

type timeOffOut struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	DateFrom string `json:"date_from"`
	DateTo   string `json:"date_to"`
	Note     string `json:"note,omitempty"`
}

func (d toolDeps) addTimeOff(ctx context.Context, req *mcp.CallToolRequest, input addTimeOffIn) (*mcp.CallToolResult, timeOffOut, error) {
	now := time.Now().UnixMilli()
	timeOff := store.TimeOff{
		ID: newID(), Kind: input.Kind, DateFrom: input.DateFrom, DateTo: input.DateTo, Note: input.Note,
		CreatedAt: now, UpdatedAt: now,
	}
	_, err := d.store.Sync(ctx, d.userID, store.SyncRequest{Changes: store.SyncChanges{TimeOff: []store.TimeOff{timeOff}}})
	if err != nil {
		return nil, timeOffOut{}, err
	}
	return nil, timeOffOut{ID: timeOff.ID, Kind: timeOff.Kind, DateFrom: timeOff.DateFrom, DateTo: timeOff.DateTo, Note: timeOff.Note}, nil
}

type listTimeOffOut struct {
	TimeOff []timeOffOut `json:"time_off"`
}

func (d toolDeps) listTimeOff(ctx context.Context, req *mcp.CallToolRequest, input any) (*mcp.CallToolResult, listTimeOffOut, error) {
	records, err := d.store.ListTimeOff(ctx, d.userID)
	if err != nil {
		return nil, listTimeOffOut{}, err
	}
	out := listTimeOffOut{TimeOff: []timeOffOut{}}
	for _, timeOff := range records {
		out.TimeOff = append(out.TimeOff, timeOffOut{
			ID: timeOff.ID, Kind: timeOff.Kind, DateFrom: timeOff.DateFrom, DateTo: timeOff.DateTo, Note: timeOff.Note,
		})
	}
	return nil, out, nil
}

type timeReportIn struct {
	From string `json:"from" jsonschema:"first day, YYYY-MM-DD"`
	To   string `json:"to" jsonschema:"last day inclusive, YYYY-MM-DD"`
	// Without an offset the days are UTC days, while the app reports on local ones, so
	// an entry started at 01:00 in UTC+3 lands in the previous day here and in the
	// right one on screen.
	TZOffsetMin *int `json:"tz_offset_min,omitempty" jsonschema:"minutes east of UTC for the days in this report; defaults to the timezone of the most recent agent session, or UTC"`
}

type reportProjectOut struct {
	Project string  `json:"project"`
	Total   string  `json:"total"`
	Hours   float64 `json:"hours"`
}

type timeReportOut struct {
	From     string                `json:"from"`
	To       string                `json:"to"`
	Total    string                `json:"total"`
	Projects []reportProjectOut    `json:"projects"`
	TimeOff  []store.TimeOffReport `json:"time_off"`
}

func (d toolDeps) timeReport(ctx context.Context, req *mcp.CallToolRequest, input timeReportIn) (*mcp.CallToolResult, timeReportOut, error) {
	fromDate, err := time.Parse(time.DateOnly, input.From)
	if err != nil {
		return nil, timeReportOut{}, fmt.Errorf("from must be YYYY-MM-DD: %w", err)
	}
	toDate, err := time.Parse(time.DateOnly, input.To)
	if err != nil {
		return nil, timeReportOut{}, fmt.Errorf("to must be YYYY-MM-DD: %w", err)
	}
	offsetMin := 0
	if input.TZOffsetMin != nil {
		offsetMin = *input.TZOffsetMin
	} else if known, err := d.store.LatestAgentTZOffset(ctx, d.userID); err != nil {
		return nil, timeReportOut{}, err
	} else if known != nil {
		// The agent sessions carry the offset of the machine this tool runs on, which
		// is a far better guess than UTC for the caller asking for "last week".
		offsetMin = *known
	}
	// The dates name local days, so the window has to start at local midnight.
	offsetMs := int64(offsetMin) * 60_000
	fromMs := fromDate.UnixMilli() - offsetMs
	toMs := toDate.AddDate(0, 0, 1).UnixMilli() - offsetMs
	report, err := d.store.BuildReport(ctx, d.userID, fromMs, toMs, offsetMin)
	if err != nil {
		return nil, timeReportOut{}, err
	}
	names, err := d.projectNames(ctx)
	if err != nil {
		return nil, timeReportOut{}, err
	}

	out := timeReportOut{From: input.From, To: input.To, Projects: []reportProjectOut{}, TimeOff: report.TimeOff}
	totalMs := int64(0)
	for _, item := range report.Projects {
		projectName := "(no project)"
		if item.ProjectID != nil {
			if name, ok := names[*item.ProjectID]; ok {
				projectName = name
			} else {
				projectName = "(deleted project)"
			}
		}
		totalMs += item.TotalMs
		out.Projects = append(out.Projects, reportProjectOut{
			Project: projectName,
			Total:   formatDuration(item.TotalMs),
			Hours:   float64(item.TotalMs) / 3600000,
		})
	}
	out.Total = formatDuration(totalMs)
	return nil, out, nil
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	return id.String()
}
