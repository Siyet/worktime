package main

import (
	"log"
	"net/http"

	"worktime/internal/api"
	"worktime/internal/config"
	"worktime/internal/store"
)

func main() {
	cfg := config.Load()

	dataStore, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer dataStore.Close()

	router := api.NewRouter(dataStore, cfg)

	if cfg.DevAuth {
		log.Print("WARNING: dev auth is enabled, all requests run as the local dev user")
	}
	log.Printf("worktime listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
	if err := http.ListenAndServe(cfg.Addr, router); err != nil {
		log.Fatal(err)
	}
}
