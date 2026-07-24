package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"worktime/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write response: %v", err)
	}
}

func (s *server) handleSync(w http.ResponseWriter, r *http.Request) {
	var request store.SyncRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	response, err := s.store.Sync(r.Context(), currentUser(r).ID, request)
	if err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("sync: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}

func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	from, errFrom := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, errTo := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if errFrom != nil || errTo != nil || to <= from {
		http.Error(w, "from and to must be unix-ms with to > from", http.StatusBadRequest)
		return
	}
	report, err := s.store.BuildReport(r.Context(), currentUser(r).ID, from, to)
	if err != nil {
		log.Printf("report: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handlePull serves read-only snapshots for debugging and simple integrations:
// GET /api/projects and GET /api/entries reuse the sync pull path with since=0.
func (s *server) handlePull(w http.ResponseWriter, r *http.Request) {
	response, err := s.store.Sync(r.Context(), currentUser(r).ID, store.SyncRequest{Since: 0})
	if err != nil {
		log.Printf("pull: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case "/api/projects":
		writeJSON(w, http.StatusOK, response.Changes.Projects)
	case "/api/entries":
		writeJSON(w, http.StatusOK, response.Changes.TimeEntries)
	default:
		writeJSON(w, http.StatusOK, response.Changes)
	}
}

type createTokenRequest struct {
	Name string `json:"name"`
}

func (s *server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var request createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Name == "" || len(request.Name) > 100 {
		http.Error(w, "name is required (1..100 chars)", http.StatusBadRequest)
		return
	}
	token, plaintext, err := s.store.CreateAPIToken(r.Context(), currentUser(r).ID, request.Name)
	if err != nil {
		log.Printf("create token: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "plaintext": plaintext})
}

func (s *server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListAPITokens(r.Context(), currentUser(r).ID)
	if err != nil {
		log.Printf("list tokens: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	tokenID := chi.URLParam(r, "id")
	if err := s.store.DeleteAPIToken(r.Context(), currentUser(r).ID, tokenID); err != nil {
		log.Printf("delete token: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
