package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/Siyet/worktime/internal/store"
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
	// The window is in milliseconds, so the caller has already pinned the day
	// boundaries; the offset only decides which calendar days the time-off rows are
	// matched against. Absent, it is UTC, which is what the boundaries then mean too.
	tzOffsetMin := 0
	if raw := r.URL.Query().Get("tz_offset_min"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < -14*60 || parsed > 14*60 {
			http.Error(w, "tz_offset_min must be minutes east of UTC within ±14h", http.StatusBadRequest)
			return
		}
		tzOffsetMin = parsed
	}
	report, err := s.store.BuildReport(r.Context(), currentUser(r).ID, from, to, tzOffsetMin)
	if err != nil {
		log.Printf("report: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handlePull serves read-only snapshots for debugging and simple integrations:
// GET /api/projects and GET /api/entries. Each reads only the table it serves - going
// through the sync path would materialise the user's whole history, tombstones
// included, and throw two thirds of it away on every request.
func (s *server) handlePull(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/projects" {
		projects, err := s.store.ListProjects(r.Context(), currentUser(r).ID)
		if err != nil {
			log.Printf("pull projects: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, projects)
		return
	}
	entries, err := s.store.ListEntries(r.Context(), currentUser(r).ID)
	if err != nil {
		log.Printf("pull entries: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleExport streams a standalone SQLite file with the current user's data.
// The full-database dump stays the server owner's job (copying the DB file);
// this is the per-user takeout.
func (s *server) handleExport(w http.ResponseWriter, r *http.Request) {
	path, cleanup, err := s.store.ExportUserDB(r.Context(), currentUser(r).ID)
	if err != nil {
		log.Printf("export: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer cleanup()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="worktime-export.sqlite"`)
	http.ServeFile(w, r, path)
}

type createTokenRequest struct {
	Name string `json:"name"`
}

const maxTokenNameLength = 100

func (s *server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var request createTokenRequest
	// Capped like every other body-reading handler: the decoder streams, but it
	// materialises the whole name string before the length check below can reject it,
	// so an unbounded body is an unbounded allocation. Characters, not bytes, so the
	// limit means what the message says for a non-ASCII name.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	if err := decoder.Decode(&request); err != nil ||
		request.Name == "" || utf8.RuneCountInString(request.Name) > maxTokenNameLength {
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
