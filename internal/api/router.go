package api

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Siyet/worktime/internal/buildinfo"
	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/lifecycle"
	"github.com/Siyet/worktime/internal/mcpserver"
	"github.com/Siyet/worktime/internal/store"
	appupdate "github.com/Siyet/worktime/internal/update"
	"github.com/Siyet/worktime/web"
)

type server struct {
	store     *store.Store
	cfg       config.Config
	lifecycle *lifecycle.Coordinator
	updates   *appupdate.Manager
}

type RouterOptions struct {
	Lifecycle *lifecycle.Coordinator
	Updates   *appupdate.Manager
}

func NewRouter(dataStore *store.Store, cfg config.Config, optional ...RouterOptions) http.Handler {
	options := RouterOptions{}
	if len(optional) > 0 {
		options = optional[0]
	}
	if options.Lifecycle == nil {
		options.Lifecycle = lifecycle.New()
	}
	if options.Updates == nil {
		info := buildinfo.Current()
		options.Updates = appupdate.NewManager(appupdate.Options{
			CurrentVersion: info.Version, Revision: info.Revision, BuiltAt: info.BuiltAt,
			ChecksEnabled: false, Policy: dataStore,
		})
	}
	s := &server{store: dataStore, cfg: cfg, lifecycle: options.Lifecycle, updates: options.Updates}

	router := chi.NewRouter()
	// No RealIP here on purpose: it overwrites RemoteAddr from X-Forwarded-For and
	// friends, which the client controls, and Caddy appends to that header rather than
	// replacing it - so a value the client supplies wins. Nothing reads the client IP
	// today; whatever adds the first rate limiter or audit log has to decide what it
	// trusts rather than inherit a spoofable value.
	router.Use(middleware.Recoverer)
	// The pull is not paginated by design - a device bootstraps in one response - so on
	// an instance with years of history that body is megabytes of JSON, which compresses
	// by roughly an order of magnitude. Without this a phone on mobile data can fail to
	// bootstrap at all: the request has a deadline, and no partial progress is possible
	// because the cursor only advances on a complete payload.
	router.Use(middleware.Compress(5))

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.lifecycle.Maintenance() {
			http.Error(w, "maintenance", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	router.Route("/auth", func(auth chi.Router) {
		auth.Get("/config", s.handleAuthConfig)
		auth.Get("/google", s.handleGoogleLogin)
		auth.With(s.lifecycle.Middleware).Get("/google/callback", s.handleGoogleCallback)
		// Signing out is a state change like any other: without this a page on a
		// sibling host can log the owner out, since SameSite=Lax does not consider it
		// cross-site. Nothing is destroyed - the local-first data survives in
		// IndexedDB - but being logged out at random is not something to leave open.
		auth.With(s.lifecycle.Middleware, s.requireSameOrigin).Post("/logout", s.handleLogout)
	})

	router.Route("/api", func(api chi.Router) {
		api.Use(s.lifecycle.Middleware)
		api.Use(s.requireAuth)
		api.Use(s.requireSameOrigin)
		api.Post("/sync", s.handleSync)
		api.Get("/me", s.handleMe)
		api.Get("/report", s.handleReport)
		api.Get("/projects", s.handleListProjects)
		api.Get("/entries", s.handleListEntries)
		api.Get("/system/version", s.handleSystemVersion)
		api.Get("/system/update", s.handleUpdateStatus)
		api.With(requireSession, s.requireAdmin).Post("/system/update/check", s.handleUpdateCheck)
		api.With(requireSession, s.requireAdmin).Put("/system/update/policy", s.handleUpdatePolicy)
		api.With(requireSession, s.requireAdmin).Post("/system/update/apply", s.handleUpdateApply)
		// Managing credentials takes the browser session: see requireSession.
		api.With(requireSession).Get("/tokens", s.handleListTokens)
		api.With(requireSession).Post("/tokens", s.handleCreateToken)
		api.With(requireSession).Delete("/tokens/{id}", s.handleDeleteToken)
		api.Get("/export.sqlite", s.handleExport)
		api.Route("/agent/sessions/{id}", func(agent chi.Router) {
			agent.Get("/status-line", s.handleAgentStatusLine)
			agent.Post("/start", s.handleAgentStart)
			agent.Post("/heartbeat", s.handleAgentHeartbeat)
			agent.Post("/stop", s.handleAgentStop)
		})
		// The integration files, served by the instance the hook will report to.
		// They carry no secrets and the same Bearer the agent already holds gets
		// them, so setup is one curl rather than "find the right branch on GitHub".
		api.Get("/agent/hook.sh", s.handleAgentHookScript)
		api.Get("/agent/statusline.sh", s.handleAgentStatusLineScript)
		api.Get("/agent/hook-settings.json", s.handleAgentHookSettings)
	})

	// MCP accepts the session cookie as well as a Bearer token, so it needs the same
	// cross-site guard as /api. A browser will not reach it today - the JSON content
	// type forces a preflight, and no CORS headers are returned - but that is the
	// browser's rule protecting the endpoint, not the endpoint protecting itself.
	mcpHandler := mcpserver.NewHandler(dataStore)
	router.With(s.lifecycle.Middleware, s.requireAuth, s.requireSameOrigin).Handle("/mcp", mcpHandler)
	router.With(s.lifecycle.Middleware, s.requireAuth, s.requireSameOrigin).Handle("/mcp/*", mcpHandler)

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
