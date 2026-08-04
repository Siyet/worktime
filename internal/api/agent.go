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

	"github.com/Siyet/worktime/internal/store"
)

type agentStartRequest struct {
	StartedAt int64   `json:"started_at"`
	Source    string  `json:"source"`
	Cwd       string  `json:"cwd"`
	GitBranch string  `json:"git_branch"`
	Model     string  `json:"model"`
	ProjectID *string `json:"project_id"`
}

type agentHeartbeatRequest struct {
	At int64 `json:"at"`
	// Activity ("prompt", "tool", "turn_end", "compact") is accepted for forward
	// compatibility but not stored yet.
	Activity string `json:"activity"`
}

type agentStopRequest struct {
	EndedAt int64  `json:"ended_at"`
	Reason  string `json:"reason"`
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

func (s *server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	var request agentStartRequest
	if !decodeAgentBody(w, r, &request) {
		return
	}
	session, err := s.store.StartAgentSession(r.Context(), currentUser(r).ID, store.AgentStart{
		SessionID: chi.URLParam(r, "id"),
		StartedAt: request.StartedAt,
		Source:    request.Source,
		Cwd:       request.Cwd,
		GitBranch: request.GitBranch,
		Model:     request.Model,
		ProjectID: request.ProjectID,
	}, s.cfg.AgentIdle.Milliseconds())
	writeAgentResult(w, session, err)
}

func (s *server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var request agentHeartbeatRequest
	if !decodeAgentBody(w, r, &request) {
		return
	}
	session, err := s.store.AgentHeartbeat(r.Context(), currentUser(r).ID,
		chi.URLParam(r, "id"), request.At, s.cfg.AgentIdle.Milliseconds())
	writeAgentResult(w, session, err)
}

func (s *server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	var request agentStopRequest
	if !decodeAgentBody(w, r, &request) {
		return
	}
	session, err := s.store.StopAgentSession(r.Context(), currentUser(r).ID,
		chi.URLParam(r, "id"), request.EndedAt, request.Reason, s.cfg.AgentIdle.Milliseconds())
	writeAgentResult(w, session, err)
}
