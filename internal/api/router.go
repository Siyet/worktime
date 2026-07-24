package api

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"worktime/internal/config"
	"worktime/internal/store"
	"worktime/web"
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
