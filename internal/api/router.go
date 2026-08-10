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
	// No RealIP here on purpose: it overwrites RemoteAddr from X-Forwarded-For and
	// friends, which the client controls, and Caddy appends to that header rather than
	// replacing it - so a value the client supplies wins. Nothing reads the client IP
	// today; whatever adds the first rate limiter or audit log has to decide what it
	// trusts rather than inherit a spoofable value.
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
		api.Use(s.requireSameOrigin)
		api.Post("/sync", s.handleSync)
		api.Get("/me", s.handleMe)
		api.Get("/report", s.handleReport)
		api.Get("/projects", s.handleListProjects)
		api.Get("/entries", s.handleListEntries)
		// Managing credentials takes the browser session: see requireSession.
		api.With(requireSession).Get("/tokens", s.handleListTokens)
		api.With(requireSession).Post("/tokens", s.handleCreateToken)
		api.With(requireSession).Delete("/tokens/{id}", s.handleDeleteToken)
		api.Get("/export.sqlite", s.handleExport)
		api.Route("/agent/sessions/{id}", func(agent chi.Router) {
			agent.Post("/start", s.handleAgentStart)
			agent.Post("/heartbeat", s.handleAgentHeartbeat)
			agent.Post("/stop", s.handleAgentStop)
		})
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
