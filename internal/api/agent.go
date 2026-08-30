package api

// Agent session endpoints: idempotent start/heartbeat/stop signals from Claude
// Code hooks (see integrations/claude-code/). All three are safe to retry and
// safe to replay from the hook's offline queue.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	claudecode "github.com/Siyet/worktime/integrations/claude-code"
	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/store"
)

type agentStartRequest struct {
	StartedAt   int64   `json:"started_at"`
	Source      string  `json:"source"`
	Cwd         string  `json:"cwd"`
	GitBranch   string  `json:"git_branch"`
	Model       string  `json:"model"`
	ProjectID   *string `json:"project_id"`
	TZOffsetMin *int    `json:"tz_offset_min"`
}

// Metadata on heartbeat and stop is optional and additive: a hook shipped before
// these fields existed keeps sending just "at" and keeps working. It exists
// because a session created by a heartbeat that outran a lost start would
// otherwise never learn its working directory or branch.
type agentHeartbeatRequest struct {
	At int64 `json:"at"`
	// Activity names what produced the signal. "tool_start" is the one the server
	// acts on: it says a tool is about to run, so the silence that follows is work
	// rather than an empty chair.
	Activity    string `json:"activity"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"git_branch"`
	Model       string `json:"model"`
	TZOffsetMin *int   `json:"tz_offset_min"`
}

type agentStopRequest struct {
	EndedAt     int64  `json:"ended_at"`
	Reason      string `json:"reason"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"git_branch"`
	Model       string `json:"model"`
	TZOffsetMin *int   `json:"tz_offset_min"`
}

func decodeAgentBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeAgentResult(w http.ResponseWriter, session store.AgentSession, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, session)
	case errors.Is(err, store.ErrInvalidInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "session not found", http.StatusNotFound)
	default:
		log.Printf("agent session: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// agentPolicy falls back to the production defaults for any duration left at zero.
func (s *server) agentPolicy() store.AgentPolicy {
	policy := store.AgentPolicy{
		IdleMs:    s.cfg.AgentIdle.Milliseconds(),
		ToolMaxMs: s.cfg.AgentToolMax.Milliseconds(),
	}
	defaults := config.Defaults()
	if policy.IdleMs <= 0 {
		policy.IdleMs = defaults.AgentIdle.Milliseconds()
	}
	if policy.ToolMaxMs <= 0 {
		policy.ToolMaxMs = defaults.AgentToolMax.Milliseconds()
	}
	return policy
}

func (s *server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	var request agentStartRequest
	if !decodeAgentBody(w, r, &request) {
		return
	}
	session, err := s.store.StartAgentSession(r.Context(), currentUser(r).ID, store.AgentStart{
		SessionID:   chi.URLParam(r, "id"),
		StartedAt:   request.StartedAt,
		Source:      request.Source,
		Cwd:         request.Cwd,
		GitBranch:   request.GitBranch,
		Model:       request.Model,
		ProjectID:   request.ProjectID,
		TZOffsetMin: request.TZOffsetMin,
	}, s.agentPolicy())
	writeAgentResult(w, session, err)
}

func (s *server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var request agentHeartbeatRequest
	if !decodeAgentBody(w, r, &request) {
		return
	}
	session, err := s.store.AgentHeartbeat(r.Context(), currentUser(r).ID, chi.URLParam(r, "id"), store.AgentSignal{
		At:          request.At,
		Kind:        request.Activity,
		Cwd:         request.Cwd,
		GitBranch:   request.GitBranch,
		Model:       request.Model,
		TZOffsetMin: request.TZOffsetMin,
	}, s.agentPolicy())
	writeAgentResult(w, session, err)
}

func (s *server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	var request agentStopRequest
	if !decodeAgentBody(w, r, &request) {
		return
	}
	session, err := s.store.StopAgentSession(r.Context(), currentUser(r).ID, chi.URLParam(r, "id"), request.Reason,
		store.AgentSignal{
			At:          request.EndedAt,
			Cwd:         request.Cwd,
			GitBranch:   request.GitBranch,
			Model:       request.Model,
			TZOffsetMin: request.TZOffsetMin,
		}, s.agentPolicy())
	writeAgentResult(w, session, err)
}

// handleAgentStatusLine is the read-only half of the Claude Code statusLine
// integration. Refreshing a terminal decoration must never count as activity:
// only lifecycle hooks may move a heartbeat or change a time entry.
func (s *server) handleAgentStatusLine(w http.ResponseWriter, r *http.Request) {
	session, err := s.store.GetAgentSession(r.Context(), currentUser(r).ID, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("agent status line: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if session.Status != "active" || session.TimeEntryID == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	entry, err := s.store.GetTimeEntry(r.Context(), currentUser(r).ID, *session.TimeEntryID)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		log.Printf("agent status line entry: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if entry.StoppedAt != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	elapsedMs := store.EntryDurationMs(entry, time.Now().UnixMilli())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "WorkTime %s · %s\n", formatAgentStatusDuration(elapsedMs), formatAgentStatusDescription(entry.Description))
}

func formatAgentStatusDuration(ms int64) string {
	duration := time.Duration(ms) * time.Millisecond
	return fmt.Sprintf("%d:%02d:%02d", int(duration.Hours()), int(duration.Minutes())%60, int(duration.Seconds())%60)
}

func formatAgentStatusDescription(description string) string {
	clean := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, description)
	clean = strings.Join(strings.Fields(clean), " ")
	if clean == "" {
		return "untitled"
	}
	runes := []rune(clean)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return clean
}

// handleAgentHookScript hands out the hook script this very binary was built
// with. The setup prompt tells an agent to fetch it from the instance it will
// report to, so a fork or an un-upgraded server never installs a hook that
// speaks a different protocol than its server.
func (s *server) handleAgentHookScript(w http.ResponseWriter, _ *http.Request) {
	serveAgentAsset(w, "text/x-shellscript; charset=utf-8", claudecode.HookScript)
}

// handleAgentStatusLineScript serves the matching terminal integration. Like
// the hook, it is fetched from the instance it reads so the client and server
// cannot drift onto different endpoint contracts.
func (s *server) handleAgentStatusLineScript(w http.ResponseWriter, _ *http.Request) {
	serveAgentAsset(w, "text/x-shellscript; charset=utf-8", claudecode.StatusLineScript)
}

// handleAgentHookSettings hands out the hook wiring for ~/.claude/settings.json.
// It is a fragment to merge, not a file to copy over: the user's settings hold
// everything else they have configured.
func (s *server) handleAgentHookSettings(w http.ResponseWriter, _ *http.Request) {
	serveAgentAsset(w, "application/json; charset=utf-8", claudecode.HookSettings)
}

func serveAgentAsset(w http.ResponseWriter, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	// Deliberately uncached: the whole point of serving these is that they match
	// the binary answering the endpoints they talk to, and an upgraded server must
	// not hand out the previous version's script.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, body)
}
