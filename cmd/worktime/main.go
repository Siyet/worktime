package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/Siyet/worktime/internal/api"
	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/store"
)

const agentReconcileInterval = time.Minute

func main() {
	cfg := config.Load()

	dataStore, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer dataStore.Close()

	router := api.NewRouter(dataStore, cfg)

	// Reconciliation closes agent sessions whose heartbeats stopped (SIGKILL, OOM,
	// network loss on the agent side). The first pass runs at startup so sessions
	// orphaned while the server itself was down are closed immediately.
	go func() {
		ticker := time.NewTicker(agentReconcileInterval)
		defer ticker.Stop()
		for {
			closed, err := dataStore.ReconcileAgentSessions(
				context.Background(), time.Now().UnixMilli(), cfg.AgentGrace.Milliseconds())
			if err != nil {
				log.Printf("agent reconcile: %v", err)
			} else if closed > 0 {
				log.Printf("agent reconcile: closed %d stale sessions", closed)
			}
			<-ticker.C
		}
	}()

	if cfg.DevAuth {
		log.Print("WARNING: dev auth is enabled, all requests run as the local dev user")
	}
	log.Printf("worktime listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
	if err := http.ListenAndServe(cfg.Addr, router); err != nil {
		log.Fatal(err)
	}
}
