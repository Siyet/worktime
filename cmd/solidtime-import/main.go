// Command solidtime-import loads a solidtime export (produced during Phase 0)
// into a running WorkTime instance through the regular sync endpoint, so the
// data flows through the same validation and LWW machinery as any client.
//
// Timestamps are derived from the source data, which makes the import
// idempotent: re-running it never overwrites rows edited in WorkTime later.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

type solidtimeExport struct {
	ExportedAt string `json:"exported_at"`
	Projects   []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Color      string `json:"color"`
		IsArchived bool   `json:"is_archived"`
	} `json:"projects"`
	TimeEntries []struct {
		ID          string  `json:"id"`
		Start       string  `json:"start"`
		End         *string `json:"end"`
		Description string  `json:"description"`
		ProjectID   *string `json:"project_id"`
	} `json:"time_entries"`
}

type project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Archived  bool   `json:"archived"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type timeEntry struct {
	ID          string  `json:"id"`
	ProjectID   *string `json:"project_id"`
	Description string  `json:"description"`
	StartedAt   int64   `json:"started_at"`
	StoppedAt   *int64  `json:"stopped_at"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

type syncChanges struct {
	Projects    []project   `json:"projects,omitempty"`
	TimeEntries []timeEntry `json:"time_entries,omitempty"`
}

type syncRequest struct {
	Since   int64       `json:"since"`
	Changes syncChanges `json:"changes"`
}

const batchSize = 5000

// pullNothingCursor makes the sync response echo back no rows.
const pullNothingCursor = int64(1) << 60

func main() {
	filePath := flag.String("file", "data/solidtime-export.json", "path to the solidtime export JSON")
	baseURL := flag.String("url", "http://localhost:8080", "WorkTime instance URL")
	token := flag.String("token", "", "API token (optional when the server runs with WORKTIME_DEV_AUTH=1)")
	flag.Parse()

	raw, err := os.ReadFile(*filePath)
	if err != nil {
		log.Fatalf("read export: %v", err)
	}
	var export solidtimeExport
	if err := json.Unmarshal(raw, &export); err != nil {
		log.Fatalf("parse export: %v", err)
	}

	exportedAt := time.Now()
	if parsed, err := time.Parse(time.RFC3339, export.ExportedAt); err == nil {
		exportedAt = parsed
	}
	exportMs := exportedAt.UnixMilli()

	projects := make([]project, 0, len(export.Projects))
	for _, source := range export.Projects {
		projects = append(projects, project{
			ID: source.ID, Name: source.Name, Color: source.Color, Archived: source.IsArchived,
			CreatedAt: exportMs, UpdatedAt: exportMs,
		})
	}

	entries := make([]timeEntry, 0, len(export.TimeEntries))
	skipped := 0
	running := 0
	for _, source := range export.TimeEntries {
		startedAt, err := time.Parse(time.RFC3339, source.Start)
		if err != nil {
			skipped++
			continue
		}
		entry := timeEntry{
			ID: source.ID, ProjectID: source.ProjectID, Description: source.Description,
			StartedAt: startedAt.UnixMilli(), CreatedAt: startedAt.UnixMilli(), UpdatedAt: startedAt.UnixMilli(),
		}
		if source.End != nil {
			if stoppedAt, err := time.Parse(time.RFC3339, *source.End); err == nil {
				stoppedMs := stoppedAt.UnixMilli()
				entry.StoppedAt = &stoppedMs
			}
		} else {
			running++
		}
		entries = append(entries, entry)
	}

	// Projects go first so entries never reference an unknown project.
	if err := push(*baseURL, *token, syncChanges{Projects: projects}); err != nil {
		log.Fatalf("push projects: %v", err)
	}
	pushed := 0
	for start := 0; start < len(entries); start += batchSize {
		end := min(start+batchSize, len(entries))
		if err := push(*baseURL, *token, syncChanges{TimeEntries: entries[start:end]}); err != nil {
			log.Fatalf("push entries %d..%d: %v", start, end, err)
		}
		pushed += end - start
		fmt.Printf("pushed %d/%d entries\n", pushed, len(entries))
	}

	fmt.Printf("done: %d projects, %d entries (%d still running, %d skipped)\n",
		len(projects), pushed, running, skipped)
	fmt.Println("note: solidtime tags are not imported - WorkTime has no tags")
}

func push(baseURL, token string, changes syncChanges) error {
	payload, err := json.Marshal(syncRequest{Since: pullNothingCursor, Changes: changes})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/sync", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2000))
		return fmt.Errorf("sync returned %d: %s", response.StatusCode, body)
	}
	return nil
}
