package update

import (
	"context"
	"net/http"

	"github.com/Siyet/worktime/internal/lifecycle"
	"github.com/Siyet/worktime/internal/store"
)

type NativeRuntime struct {
	Packaging     string
	Executable    string
	DatabasePath  string
	DataDirectory string
	Store         *store.Store
	Lifecycle     *lifecycle.Coordinator
	HTTPClient    *http.Client
	// CapabilityProbe is injected by tests only. Production leaves it nil so the
	// Linux installer probes namespaces and atomic exchange on the real paths.
	CapabilityProbe func(context.Context) error
	// Quiesce stops background writers while Store remains open for a
	// transactionally consistent rollback backup.
	Quiesce func(context.Context) error
	// Resume restarts writers if update preparation fails before the server and
	// Store have been shut down.
	Resume func()
	// Prepare stops the HTTP server and closes Store immediately before the
	// executable exchange. A successful call is followed only by exec or rollback.
	Prepare func(context.Context) error
	// HandoffFailed releases the main goroutine only when exec unexpectedly
	// returns, allowing the process supervisor to restart a fail-closed instance.
	HandoffFailed func(error)
}

func NewNativeInstaller(runtime NativeRuntime) Installer {
	return newNativeInstaller(runtime)
}

func nativePackagingSupport(packaging string) (bool, string) {
	switch packaging {
	case "native":
		return true, ""
	case "docker":
		return false, "This Docker installation must be updated by pulling the signed manifest image digest and recreating the container."
	default:
		return false, "Self-apply is available only to an official standalone native release build."
	}
}
