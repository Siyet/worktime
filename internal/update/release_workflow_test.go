package update

import (
	"os"
	"strings"
	"testing"
)

func releaseWorkflow(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	return string(data)
}

func TestReleaseCleanupCoversOwnPublicNonImmutableFailure(t *testing.T) {
	workflow := releaseWorkflow(t)
	for _, required := range []string{
		"cleanup_armed=true",
		`release_id_file="$RUNNER_TEMP/worktime-release-id"`,
		`release_id="$(cat "$release_id_file")"`,
		`releases/$release_id`,
		"String(r.id)===process.argv[2]",
		"r.immutable===false",
		"r.tag_name===process.env.VERSION",
		"r.target_commitish===process.env.SOURCE_SHA",
		`if test "$tag_sha" = "$SOURCE_SHA"`,
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("release cleanup lost required ownership guard %q", required)
		}
	}
	predicate := strings.Index(workflow, "r.immutable===false")
	deleteRelease := strings.Index(workflow[predicate:], `gh api --method DELETE "repos/$GITHUB_REPOSITORY/releases/$release_id"`)
	if predicate < 0 || deleteRelease < 0 {
		t.Fatal("public non-immutable release is not covered by own-run cleanup")
	}
	predicateLineEnd := strings.Index(workflow[predicate:], "\n")
	if predicateLineEnd < 0 || strings.Contains(workflow[predicate:predicate+predicateLineEnd], "r.draft") {
		t.Fatal("failure cleanup still requires a draft")
	}
}

func TestReleaseWorkflowDoesNotUseRunnerContextInJobEnvironment(t *testing.T) {
	workflow := releaseWorkflow(t)
	jobs := strings.Index(workflow, "jobs:")
	steps := strings.Index(workflow, "    steps:")
	if jobs < 0 || steps < 0 || jobs >= steps {
		t.Fatal("release workflow does not have the expected jobs/env/steps structure")
	}
	if strings.Contains(workflow[jobs:steps], "${{ runner.") {
		t.Fatal("runner context is unavailable while GitHub parses a job-level environment")
	}
	initialize := strings.Index(workflow, `release_id_file="$RUNNER_TEMP/worktime-release-id"`)
	firstUse := strings.Index(workflow, `test -s "$release_id_file"`)
	if initialize < 0 || firstUse < 0 || initialize >= firstUse {
		t.Fatal("release ID path must be initialized from RUNNER_TEMP before cleanup uses it")
	}
}

func TestReleaseCleanupDisarmsOnlyAfterLateVerification(t *testing.T) {
	workflow := releaseWorkflow(t)
	published := strings.Index(workflow, `gh api --method PATCH "repos/$GITHUB_REPOSITORY/releases/$release_id" -F draft=false`)
	immutable := strings.Index(workflow, "if(r.draft || !r.immutable")
	assetVerification := strings.LastIndex(workflow, "gh release verify-asset")
	disarm := strings.LastIndex(workflow, "cleanup_armed=false")
	if published < 0 || immutable <= published || assetVerification <= immutable || disarm <= assetVerification {
		t.Fatalf("cleanup disarmed before late release verification: publish=%d immutable=%d assets=%d disarm=%d", published, immutable, assetVerification, disarm)
	}
}
