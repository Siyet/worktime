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

	"worktime/internal/authctx"
	"worktime/internal/store"
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

	return server
}

// --- shared helpers ---

func (d toolDeps) pushEntries(ctx context.Context, entries ...store.TimeEntry) error {
	_, err := d.store.Sync(ctx, d.userID, store.SyncRequest{Changes: store.SyncChanges{TimeEntries: entries}})
	return err
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
	entry.UpdatedAt = now
	if err := d.pushEntries(ctx, entry); err != nil {
		return nil, timerOut{}, err
	}
	return nil, timerOut{
		EntryID: entry.ID, Description: entry.Description,
		StartedAt: time.UnixMilli(entry.StartedAt).Format(time.RFC3339),
		Elapsed:   formatDuration(now - entry.StartedAt),
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
		entry.UpdatedAt = now
		updated = append(updated, entry)
	}
	if len(updated) > 0 {
		if err := d.pushEntries(ctx, updated...); err != nil {
			return nil, stopAllOut{}, err
		}
	}
	return nil, stopAllOut{StoppedCount: len(updated)}, nil
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
		out.Timers = append(out.Timers, timerOut{
			EntryID: entry.ID, Description: entry.Description, Project: projectName,
			StartedAt: time.UnixMilli(entry.StartedAt).Format(time.RFC3339),
			Elapsed:   formatDuration(now - entry.StartedAt),
		})
	}
	return nil, out, nil
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
	report, err := d.store.BuildReport(ctx, d.userID, fromDate.UnixMilli(), toDate.AddDate(0, 0, 1).UnixMilli())
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
