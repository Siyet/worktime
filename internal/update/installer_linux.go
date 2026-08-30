//go:build linux

package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func newNativeInstaller(nativeRuntime NativeRuntime) Installer {
	if supported, reason := nativePackagingSupport(nativeRuntime.Packaging); !supported {
		return &unsupportedNativeInstaller{reason: reason}
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return &unsupportedNativeInstaller{reason: "Self-update v1 supports standalone Linux amd64/arm64 only."}
	}
	if nativeRuntime.Executable == "" || nativeRuntime.DatabasePath == "" || nativeRuntime.DataDirectory == "" ||
		nativeRuntime.Store == nil || nativeRuntime.Lifecycle == nil || nativeRuntime.Quiesce == nil ||
		nativeRuntime.Resume == nil || nativeRuntime.Prepare == nil || nativeRuntime.HandoffFailed == nil {
		return &unsupportedNativeInstaller{reason: "Native self-update is unavailable because the runtime cannot provide an exclusive maintenance handoff."}
	}
	probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probeNativeCapabilities(probeContext, nativeRuntime); err != nil {
		return &unsupportedNativeInstaller{reason: "Native self-update capability probe failed; this installation is notification-only: " + err.Error()}
	}
	return newExecutableInstaller(nativeRuntime, atomicExchange, processExec)
}

func probeNativeCapabilities(ctx context.Context, nativeRuntime NativeRuntime) error {
	if nativeRuntime.CapabilityProbe != nil {
		return nativeRuntime.CapabilityProbe(ctx)
	}
	if err := probeAtomicExchange(filepath.Dir(nativeRuntime.Executable), atomicExchange); err != nil {
		return fmt.Errorf("atomic executable exchange: %w", err)
	}
	updateDirectory := filepath.Join(nativeRuntime.DataDirectory, "update")
	if err := os.MkdirAll(updateDirectory, 0o700); err != nil {
		return fmt.Errorf("private preflight directory: %w", err)
	}
	privateDirectory, err := os.MkdirTemp(updateDirectory, "capability-*")
	if err != nil {
		return fmt.Errorf("private preflight directory: %w", err)
	}
	defer os.RemoveAll(privateDirectory)
	output, err := runCommand(ctx, nativeRuntime.Executable, []string{"--version"}, isolatedEnvironment(privateDirectory, ""), privateDirectory)
	if err != nil {
		return fmt.Errorf("isolated no-egress preflight namespace: %w: %s", err, output)
	}
	return nil
}

type unsupportedNativeInstaller struct{ reason string }

func (installer *unsupportedNativeInstaller) Supported() (bool, string) {
	return false, installer.reason
}
func (installer *unsupportedNativeInstaller) Apply(context.Context, Manifest, Asset) error {
	return fmt.Errorf("%s", installer.reason)
}
