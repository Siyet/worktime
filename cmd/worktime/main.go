package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Siyet/worktime/internal/api"
	"github.com/Siyet/worktime/internal/buildinfo"
	"github.com/Siyet/worktime/internal/config"
	"github.com/Siyet/worktime/internal/lifecycle"
	"github.com/Siyet/worktime/internal/store"
	appupdate "github.com/Siyet/worktime/internal/update"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		info := buildinfo.Current()
		fmt.Printf("worktime %s (revision %s, built %s)\n", info.Version, info.Revision, info.BuiltAt)
		return
	}

	cfg := config.Load()
	if len(os.Args) == 2 && os.Args[1] == "--update-self-check" {
		dataStore, err := store.Open(cfg.DBPath)
		if err != nil {
			log.Fatalf("self-check store: %v", err)
		}
		if _, err := dataStore.UserVersion(context.Background()); err != nil {
			_ = dataStore.Close()
			log.Fatalf("self-check schema: %v", err)
		}
		if err := dataStore.CheckIntegrity(context.Background()); err != nil {
			_ = dataStore.Close()
			log.Fatalf("self-check integrity: %v", err)
		}
		if err := dataStore.Close(); err != nil {
			log.Fatalf("self-check close: %v", err)
		}
		return
	}

	absoluteDB, err := filepath.Abs(cfg.DBPath)
	if err != nil {
		log.Fatalf("resolve database path: %v", err)
	}
	cfg.DBPath = absoluteDB
	dataDirectory := filepath.Dir(cfg.DBPath)
	executable, err := os.Executable()
	if err != nil {
		log.Fatalf("resolve executable: %v", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		log.Fatalf("resolve executable symlinks: %v", err)
	}
	bootstrapping, err := appupdate.RecoverStartup(executable, cfg.DBPath, dataDirectory)
	if err != nil {
		log.Fatalf("recover interrupted update: %v", err)
	}

	dataStore, err := store.Open(cfg.DBPath)
	if err != nil {
		if bootstrapping {
			if rollbackErr := appupdate.RollbackStartup(dataDirectory); rollbackErr != nil {
				log.Fatalf("open store after update: %v; rollback: %v", err, rollbackErr)
			}
		}
		log.Fatalf("open store: %v", err)
	}
	defer dataStore.Close()
	if bootstrapping {
		if err := dataStore.CheckIntegrity(context.Background()); err != nil {
			_ = dataStore.Close()
			if rollbackErr := appupdate.RollbackStartup(dataDirectory); rollbackErr != nil {
				log.Fatalf("verify store after update: %v; rollback: %v", err, rollbackErr)
			}
		}
	}

	coordinator := lifecycle.New()
	if bootstrapping {
		// Do not admit any store user until the updated process has proved it can
		// construct the runtime, bind the configured address and pass readiness.
		coordinator.CloseAdmission()
	}
	jobsContext, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()
	var jobs sync.WaitGroup
	var server *http.Server
	handoffFailure := make(chan error, 1)
	var prepareOnce sync.Once
	var prepareErr error
	prepareRuntime := func(ctx context.Context) error {
		prepareOnce.Do(func() {
			cancelJobs()
			jobs.Wait()
			if server != nil {
				shutdownContext, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				prepareErr = server.Shutdown(shutdownContext)
			}
			if closeErr := dataStore.Close(); prepareErr == nil {
				prepareErr = closeErr
			}
		})
		return prepareErr
	}
	info := buildinfo.Current()
	nativeInstaller := appupdate.NewNativeInstaller(appupdate.NativeRuntime{
		Packaging: info.Packaging, Executable: executable, DatabasePath: cfg.DBPath,
		DataDirectory: dataDirectory, Store: dataStore, Lifecycle: coordinator,
		// Background jobs participate in the lifecycle gate below, so draining the
		// gate is sufficient to quiesce the database without cancelling jobs early.
		Quiesce: func(context.Context) error { return nil }, Resume: func() {},
		Prepare: prepareRuntime, HandoffFailed: func(err error) { handoffFailure <- err },
	})
	updateManager := appupdate.NewManager(appupdate.Options{
		CurrentVersion: info.Version, Revision: info.Revision, BuiltAt: info.BuiltAt,
		DataDirectory: dataDirectory, ChecksEnabled: cfg.UpdateChecks, Policy: dataStore,
		Verifier: appupdate.NewSigstoreVerifier(dataDirectory), Installer: nativeInstaller,
	})
	router := api.NewRouter(dataStore, cfg, api.RouterOptions{Lifecycle: coordinator, Updates: updateManager})
	// Explicit timeouts: without them a client that dribbles a request holds a
	// goroutine and a file descriptor for as long as it likes. The documented
	// self-hosting path in the README publishes this port directly, with no proxy in
	// front to absorb that.
	//
	// There is deliberately no WriteTimeout: /mcp streams responses and
	// /api/export.sqlite serves a whole database file, and both are legitimately slow.
	// Publish the server before starting background update checks: an immediate
	// automatic update may otherwise race with this assignment during handoff.
	server = &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	listener, err := acquireStartupListener(cfg.Addr, bootstrapping, func() error {
		closeErr := dataStore.Close()
		rollbackErr := appupdate.RollbackStartup(dataDirectory)
		return errors.Join(closeErr, rollbackErr)
	})
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.Addr, err)
	}
	if err := dataStore.CheckIntegrity(context.Background()); err != nil {
		_ = listener.Close()
		if bootstrapping {
			_ = dataStore.Close()
			if rollbackErr := appupdate.RollbackStartup(dataDirectory); rollbackErr != nil {
				log.Fatalf("readiness after update: %v; rollback: %v", err, rollbackErr)
			}
		}
		log.Fatalf("startup readiness: %v", err)
	}
	if bootstrapping {
		if err := appupdate.CommitStartup(dataDirectory); err != nil {
			_ = listener.Close()
			log.Fatalf("commit update bootstrap: %v", err)
		}
		coordinator.OpenAdmission()
	}

	// Reconciliation closes agent sessions whose heartbeats stopped (SIGKILL, OOM,
	// network loss on the agent side). The first pass runs at startup so sessions
	// orphaned while the server itself was down are closed immediately. Expired
	// sessions are swept on the same tick - they are harmless (every lookup filters on
	// expires_at) but nothing else would ever remove them.
	jobs.Add(1)
	go func() {
		defer jobs.Done()
		ticker := time.NewTicker(cfg.AgentReconcile)
		defer ticker.Stop()
		for {
			if release, admitted := coordinator.Lease(); admitted {
				closed, err := dataStore.ReconcileAgentSessions(
					jobsContext, time.Now().UnixMilli(), cfg.AgentGrace.Milliseconds())
				if jobsContext.Err() != nil {
					release()
					return
				}
				if err != nil {
					log.Printf("agent reconcile: %v", err)
				} else if closed > 0 {
					log.Printf("agent reconcile: closed %d stale sessions", closed)
				}
				if err := dataStore.DeleteExpiredSessions(jobsContext); err != nil && jobsContext.Err() == nil {
					log.Printf("session sweep: %v", err)
				}
				release()
			}
			select {
			case <-jobsContext.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	if cfg.UpdateChecks {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			check := func() {
				release, admitted := coordinator.Lease()
				if !admitted {
					return
				}
				defer release()
				checkContext, cancel := context.WithTimeout(jobsContext, 45*time.Second)
				defer cancel()
				if err := updateManager.Check(checkContext); err != nil && jobsContext.Err() == nil {
					log.Printf("update check: %v", err)
					return
				}
				status := updateManager.Status(checkContext, false)
				if status.AutoApply && status.UpdateAvailable && status.ApplyMode == "automatic" {
					if err := updateManager.Apply(checkContext); err != nil {
						log.Printf("automatic update: %v", err)
					}
				}
			}
			check()
			ticker := time.NewTicker(6 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-jobsContext.Done():
					return
				case <-ticker.C:
					check()
				}
			}
		}()
	}

	if cfg.DevAuth {
		log.Print("WARNING: dev auth is enabled, all requests run as the local dev user")
	}
	log.Printf("worktime %s (revision %s) listening on %s (db: %s)", info.Version, info.Revision, cfg.Addr, cfg.DBPath)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	if coordinator.Maintenance() {
		log.Fatalf("self-update handoff: %v", <-handoffFailure)
	}
}

func acquireStartupListener(address string, bootstrapping bool, rollback func() error) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err == nil || !bootstrapping {
		return listener, err
	}
	return nil, errors.Join(err, rollback())
}
