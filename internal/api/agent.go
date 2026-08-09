package api

// Agent session endpoints: idempotent start/heartbeat/stop signals from Claude
// Code hooks (see integrations/claude-code/). All three are safe to retry and
// safe to replay from the hook's offline queue.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

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
// A zero MaxPauseMs makes every gap past the idle threshold split the entry in two,
// which is the opposite of the documented "a session owns exactly one time entry" -
// config.Load can never produce it, but a Config built by hand in a test can, and then
// the test pins behaviour no deployment has.
func (s *server) agentPolicy() store.AgentPolicy {
	policy := store.AgentPolicy{
		IdleMs:     s.cfg.AgentIdle.Milliseconds(),
		ToolMaxMs:  s.cfg.AgentToolMax.Milliseconds(),
		MaxPauseMs: s.cfg.AgentMaxPause.Milliseconds(),
	}
	defaults := config.Defaults()
	if policy.IdleMs <= 0 {
		policy.IdleMs = defaults.AgentIdle.Milliseconds()
	}
	if policy.ToolMaxMs <= 0 {
		policy.ToolMaxMs = defaults.AgentToolMax.Milliseconds()
	}
	if policy.MaxPauseMs <= 0 {
		policy.MaxPauseMs = defaults.AgentMaxPause.Milliseconds()
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
