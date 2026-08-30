package store

import (
	"path/filepath"
	"testing"
)

func TestAutoUpdatePolicyDefaultsOffAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-policy.db")
	dataStore, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	enabled, err := dataStore.AutoUpdate(t.Context())
	if err != nil || enabled {
		t.Fatalf("default policy = %v, %v", enabled, err)
	}
	if err := dataStore.SetAutoUpdate(t.Context(), true); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	dataStore, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer dataStore.Close()
	enabled, err = dataStore.AutoUpdate(t.Context())
	if err != nil || !enabled {
		t.Fatalf("persisted policy = %v, %v", enabled, err)
	}
}
