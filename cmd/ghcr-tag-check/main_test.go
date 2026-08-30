package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Siyet/worktime/internal/releaseguard"
)

func TestWriteGitHubOutputRecordsAbsentAndExistingTags(t *testing.T) {
	output := filepath.Join(t.TempDir(), "github-output")
	if err := os.WriteFile(output, []byte("previous=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeGitHubOutput(output, releaseguard.TagResolution{}); err != nil {
		t.Fatalf("write absent tag output: %v", err)
	}
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := writeGitHubOutput(output, releaseguard.TagResolution{Exists: true, Digest: digest}); err != nil {
		t.Fatalf("write existing tag output: %v", err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := "previous=value\nexists=false\ndigest=\nexists=true\ndigest=" + digest + "\n"
	if string(data) != want {
		t.Fatalf("unexpected GitHub output:\n%s\nwant:\n%s", data, want)
	}
}

func TestWriteGitHubOutputRequiresRunnerFile(t *testing.T) {
	if err := writeGitHubOutput("", releaseguard.TagResolution{}); err == nil {
		t.Fatal("expected missing GITHUB_OUTPUT to fail closed")
	}
}
