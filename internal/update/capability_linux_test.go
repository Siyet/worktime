//go:build linux

package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siyet/worktime/internal/lifecycle"
	"github.com/Siyet/worktime/internal/store"
)

func TestRealLinuxCapabilityProbeDoesNotReplaceExecutable(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "worktime-probe")
	contents := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(executable, contents, 0o755); err != nil {
		t.Fatalf("write probe executable: %v", err)
	}
	err := probeNativeCapabilities(t.Context(), NativeRuntime{
		Executable: executable, DataDirectory: directory,
	})
	after, readErr := os.ReadFile(executable)
	if readErr != nil || string(after) != string(contents) {
		t.Fatalf("capability probe changed its executable: data=%q err=%v", after, readErr)
	}
	if err != nil {
		// Kernels that disable unprivileged user/network namespaces are expected to
		// become notification-only. The real probe still ran and preserved the file.
		t.Logf("runner is notification-only: %v", err)
	}
}

func TestCapabilityProbeFailureProducesPreciseNotificationOnlyStatus(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := store.Open(filepath.Join(directory, "worktime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer dataStore.Close()
	installer := newNativeInstaller(NativeRuntime{
		Packaging: "native", Executable: filepath.Join(directory, "worktime"),
		DatabasePath: filepath.Join(directory, "worktime.db"), DataDirectory: directory,
		Store: dataStore, Lifecycle: lifecycle.New(),
		Quiesce: func(context.Context) error { return nil }, Resume: func() {},
		Prepare: func(context.Context) error { return nil }, HandoffFailed: func(error) {},
		CapabilityProbe: func(context.Context) error { return errors.New("network namespace denied") },
	})
	supported, reason := installer.Supported()
	if supported || !strings.Contains(reason, "notification-only") || !strings.Contains(reason, "network namespace denied") {
		t.Fatalf("capability failure did not produce precise notification-only status: supported=%v reason=%q", supported, reason)
	}
}
