package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

func (s *server) handleSystemVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.updates.Version())
}

func (s *server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.updates.Status(r.Context(), s.isAdmin(r)))
}

func (s *server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.updates.Check(r.Context()); err != nil {
		log.Printf("update check: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, s.updates.Status(r.Context(), true))
}

func (s *server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	request := struct {
		AutoApply *bool `json:"auto_apply"`
	}{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || request.AutoApply == nil {
		http.Error(w, "invalid update policy", http.StatusBadRequest)
		return
	}
	if err := s.updates.SetAutoApply(r.Context(), *request.AutoApply); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.updates.Status(r.Context(), true))
}

func (s *server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if err := s.updates.Apply(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusAccepted, s.updates.Status(r.Context(), true))
}
