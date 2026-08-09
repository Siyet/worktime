package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBuildReport(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "report@test.local")
	ctx := context.Background()

	projectID := uuid.NewString()
	otherProjectID := uuid.NewString()
	hour := time.Hour.Milliseconds()
	baseTime := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC).UnixMilli()

	push := SyncRequest{Changes: SyncChanges{
		Projects: []Project{
			{ID: projectID, Name: "Main", UpdatedAt: 1, CreatedAt: 1},
			{ID: otherProjectID, Name: "Side", UpdatedAt: 1, CreatedAt: 1},
		},
		TimeEntries: []TimeEntry{
			// Two hours on Main, one hour on Side, all inside the window.
			{ID: uuid.NewString(), ProjectID: &projectID, StartedAt: baseTime,
				StoppedAt: msPointer(baseTime + 2*hour), CreatedAt: 1, UpdatedAt: 1},
			{ID: uuid.NewString(), ProjectID: &otherProjectID, StartedAt: baseTime + 3*hour,
				StoppedAt: msPointer(baseTime + 4*hour), CreatedAt: 1, UpdatedAt: 1},
			// Running timer: excluded from reports.
			{ID: uuid.NewString(), ProjectID: &projectID, StartedAt: baseTime + 5*hour, CreatedAt: 1, UpdatedAt: 1},
			// Outside the window: excluded.
			{ID: uuid.NewString(), ProjectID: &projectID, StartedAt: baseTime - 48*hour,
				StoppedAt: msPointer(baseTime - 46*hour), CreatedAt: 1, UpdatedAt: 1},
		},
		TimeOff: []TimeOff{
			// Three days of vacation, two of them inside the report window.
			{ID: uuid.NewString(), Kind: "vacation", DateFrom: "2026-07-05", DateTo: "2026-07-07",
				CreatedAt: 1, UpdatedAt: 1},
		},
	}}
	if _, err := testStore.Sync(ctx, user.ID, push); err != nil {
		t.Fatalf("push: %v", err)
	}

	windowFrom := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC).UnixMilli()
	windowTo := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC).UnixMilli()
	report, err := testStore.BuildReport(ctx, user.ID, windowFrom, windowTo, 0)
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	if len(report.Projects) != 2 {
		t.Fatalf("expected 2 project rows, got %+v", report.Projects)
	}
	if *report.Projects[0].ProjectID != projectID || report.Projects[0].TotalMs != 2*hour {
		t.Fatalf("expected Main with 2h on top, got %+v", report.Projects[0])
	}
	if *report.Projects[1].ProjectID != otherProjectID || report.Projects[1].TotalMs != hour {
		t.Fatalf("expected Side with 1h, got %+v", report.Projects[1])
	}

	if len(report.TimeOff) != 1 || report.TimeOff[0].Kind != "vacation" || report.TimeOff[0].Days != 2 {
		t.Fatalf("expected 2 vacation days inside window, got %+v", report.TimeOff)
	}
}

func TestBuildReportExcludesAgentPause(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "report-pause@test.local")
	ctx := context.Background()

	// One agent session: a minute of work, a long gap, another minute, stop.
	// The server report has to bill the two minutes, not the whole interval.
	sessionID := uuid.NewString()
	startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	resumed := agentBaseMs + 60_000 + 20*60_000
	testHeartbeat(t, testStore, user.ID, sessionID, resumed)
	testHeartbeat(t, testStore, user.ID, sessionID, resumed+60_000)
	testStop(t, testStore, user.ID, sessionID, resumed+60_000, "other")

	report, err := testStore.BuildReport(ctx, user.ID, agentBaseMs-1000, resumed+120_000, 0)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(report.Projects) != 1 {
		t.Fatalf("expected one project row, got %+v", report.Projects)
	}
	if report.Projects[0].TotalMs != 120_000 {
		t.Fatalf("expected two billed minutes, got %d", report.Projects[0].TotalMs)
	}
}
