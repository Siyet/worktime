package main

import (
	"log"
	"net/http"
	"os"

	"worktime/internal/api"
)

func main() {
	addr := os.Getenv("WORKTIME_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	router := api.NewRouter()

	log.Printf("worktime listening on %s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatal(err)
	}
}
