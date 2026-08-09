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
	// orphaned while the server itself was down are closed immediately. Expired
	// sessions are swept on the same tick - they are harmless (every lookup filters on
	// expires_at) but nothing else would ever remove them.
	go func() {
		ticker := time.NewTicker(cfg.AgentReconcile)
		defer ticker.Stop()
		for {
			closed, err := dataStore.ReconcileAgentSessions(
				context.Background(), time.Now().UnixMilli(), cfg.AgentGrace.Milliseconds())
			if err != nil {
				log.Printf("agent reconcile: %v", err)
			} else if closed > 0 {
				log.Printf("agent reconcile: closed %d stale sessions", closed)
			}
			if err := dataStore.DeleteExpiredSessions(context.Background()); err != nil {
				log.Printf("session sweep: %v", err)
			}
			<-ticker.C
		}
	}()

	if cfg.DevAuth {
		log.Print("WARNING: dev auth is enabled, all requests run as the local dev user")
	}
	log.Printf("worktime listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
	// Explicit timeouts: without them a client that dribbles a request holds a
	// goroutine and a file descriptor for as long as it likes. The documented
	// self-hosting path in the README publishes this port directly, with no proxy in
	// front to absorb that.
	//
	// There is deliberately no WriteTimeout: /mcp streams responses and
	// /api/export.sqlite serves a whole database file, and both are legitimately slow.
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
