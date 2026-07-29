package api

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/mcpserver"
	"github.com/Siyet/worktime/internal/store"
	"github.com/Siyet/worktime/web"
)

type server struct {
	store *store.Store
	cfg   config.Config
}

func NewRouter(dataStore *store.Store, cfg config.Config) http.Handler {
	s := &server{store: dataStore, cfg: cfg}

	router := chi.NewRouter()
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	router.Route("/auth", func(auth chi.Router) {
		auth.Get("/config", s.handleAuthConfig)
		auth.Get("/google", s.handleGoogleLogin)
		auth.Get("/google/callback", s.handleGoogleCallback)
		auth.Post("/logout", s.handleLogout)
	})

	router.Route("/api", func(api chi.Router) {
		api.Use(s.requireAuth)
		api.Post("/sync", s.handleSync)
		api.Get("/me", s.handleMe)
		api.Get("/report", s.handleReport)
		api.Get("/projects", s.handlePull)
		api.Get("/entries", s.handlePull)
		api.Get("/tokens", s.handleListTokens)
		api.Post("/tokens", s.handleCreateToken)
		api.Delete("/tokens/{id}", s.handleDeleteToken)
	})

	mcpHandler := mcpserver.NewHandler(dataStore)
	router.With(s.requireAuth).Handle("/mcp", mcpHandler)
	router.With(s.requireAuth).Handle("/mcp/*", mcpHandler)

	router.NotFound(spaHandler())

	return router
}

// spaHandler serves the embedded frontend build with an index.html fallback,
// so client-side routes keep working on hard reload.
func spaHandler() http.HandlerFunc {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(dist))

	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path != "/" {
			if _, statErr := fs.Stat(dist, path[1:]); statErr != nil {
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	}
}
