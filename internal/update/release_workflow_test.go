package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
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
	if strings.Contains(workflow, "RELEASE_ID_FILE") {
		t.Fatal("workflow must not retain the invalid job-level release ID variable")
	}
	initialize := strings.Index(workflow, `release_id_file="$RUNNER_TEMP/worktime-release-id"`)
	firstUse := strings.Index(workflow, `test -s "$release_id_file"`)
	if initialize < 0 || firstUse < 0 || initialize >= firstUse {
		t.Fatal("release ID path must be initialized from RUNNER_TEMP before cleanup uses it")
	}
	if !strings.Contains(workflow, `"$release_json" > "$release_id_file"`) {
		t.Fatal("created release ID must be written to the step-local release ID path")
	}
	if count := strings.Count(workflow, "$release_id_file"); count != 4 {
		t.Fatalf("release ID path must have exactly four uses, got %d", count)
	}
	if count := strings.Count(workflow, `release_id="$(cat "$release_id_file")"`); count != 2 {
		t.Fatalf("release ID path must be read exactly twice using its lowercase name, got %d", count)
	}
}

func TestReleaseVersionIsIsolatedFromValidationBuild(t *testing.T) {
	workflow := releaseWorkflow(t)
	validationStart := strings.Index(workflow, "      - name: Run full validation gates")
	nativeStart := strings.Index(workflow, "      - name: Build and verify native artifacts")
	if validationStart < 0 || nativeStart <= validationStart {
		t.Fatal("release workflow does not have the expected validation and native artifact steps")
	}
	validation := workflow[validationStart:nativeStart]
	unsetVersion := strings.Index(validation, "          unset VERSION\n")
	firstBuild := strings.Index(validation, "          npm ci --no-fund --no-audit --prefix web\n")
	if unsetVersion < 0 || firstBuild < 0 || unsetVersion >= firstBuild {
		t.Fatal("release input VERSION must be unset before validation installs or builds anything")
	}
	validationRemainder := validation[unsetVersion+len("          unset VERSION\n"):]
	if strings.Contains(validationRemainder, "VERSION") {
		t.Fatal("release input VERSION must not be referenced or reassigned after validation isolation")
	}
	for _, command := range []string{
		"          npm run build --prefix web\n",
		"          make build\n",
		"          (cd web && npx playwright test)\n",
	} {
		commandIndex := strings.Index(validation, command)
		if commandIndex <= unsetVersion {
			t.Fatalf("validation command must run after VERSION is unset: %q", strings.TrimSpace(command))
		}
	}

	nativeEnd := strings.Index(workflow[nativeStart:], "      - name: Resolve existing container image tag")
	if nativeEnd < 0 {
		t.Fatal("release workflow does not have the expected post-native step")
	}
	native := workflow[nativeStart : nativeStart+nativeEnd]
	for _, required := range []string{
		`VITE_WORKTIME_VERSION="$VERSION" npm run build --prefix web`,
		"buildinfo.Version=$VERSION",
	} {
		if !strings.Contains(native, required) {
			t.Fatalf("native release build lost version propagation %q", required)
		}
	}
	releaseWebBuild := strings.Index(native, `VITE_WORKTIME_VERSION="$VERSION" npm run build --prefix web`)
	nativeBuildLoop := strings.Index(native, "          for architecture in amd64 arm64; do\n")
	if releaseWebBuild < 0 || nativeBuildLoop < 0 || releaseWebBuild >= nativeBuildLoop {
		t.Fatal("release frontend must be rebuilt with the release version before native binaries embed it")
	}

	imageStart := strings.Index(workflow, "      - name: Build and push multi-platform image")
	if imageStart < 0 {
		t.Fatal("release workflow does not have a separate image build step")
	}
	imageEnd := strings.Index(workflow[imageStart:], "      - name: Sign multi-platform image")
	if imageEnd < 0 {
		t.Fatal("release workflow does not have separate image build and signing steps")
	}
	image := workflow[imageStart : imageStart+imageEnd]
	for _, required := range []string{
		`--build-arg "VERSION=$VERSION"`,
		`--tag "$IMAGE:$VERSION"`,
	} {
		if !strings.Contains(image, required) {
			t.Fatalf("container release build lost version propagation %q", required)
		}
	}
}

func TestReleaseImageBuildIsBoundedObservableAndRetrySafe(t *testing.T) {
	workflow := releaseWorkflow(t)
	guardStart := strings.Index(workflow, "      - name: Resolve existing container image tag")
	imageStart := strings.Index(workflow, "      - name: Build and push multi-platform image")
	signStart := strings.Index(workflow, "      - name: Sign multi-platform image")
	reuseStart := strings.Index(workflow, "      - name: Verify and reuse signed multi-platform image")
	manifestStart := strings.Index(workflow, "      - name: Enforce manifest generation and create signed manifest")
	if guardStart < 0 || imageStart <= guardStart || signStart <= imageStart || reuseStart <= signStart || manifestStart <= reuseStart {
		t.Fatalf("container resolve/build/sign/reuse order is unsafe: resolve=%d image=%d sign=%d reuse=%d manifest=%d", guardStart, imageStart, signStart, reuseStart, manifestStart)
	}
	guard := workflow[guardStart:imageStart]
	for _, required := range []string{
		"        id: container-image\n",
		"          go run ./cmd/ghcr-tag-check\n",
	} {
		if !strings.Contains(guard, required) {
			t.Fatalf("container resolver is missing %q", required)
		}
	}
	for _, forbidden := range []string{"imagetools inspect", "grep -Eq", "manifest unknown", "credential"} {
		if strings.Contains(guard, forbidden) {
			t.Fatalf("container retry guard must not classify text or depend on a credential helper: %q", forbidden)
		}
	}
	image := workflow[imageStart:signStart]
	for _, required := range []string{
		"        if: steps.container-image.outputs.exists == 'false'\n",
		"        timeout-minutes: 30\n",
		"docker buildx build --progress=plain",
		`--platform linux/amd64,linux/arm64`,
		`--push --metadata-file dist/image-metadata.json`,
		`echo "IMAGE_DIGEST=$IMAGE_DIGEST" >> "$GITHUB_ENV"`,
	} {
		if !strings.Contains(image, required) {
			t.Fatalf("bounded observable image build is missing %q", required)
		}
	}
	sign := workflow[signStart:reuseStart]
	for _, required := range []string{
		"        if: steps.container-image.outputs.exists == 'false'\n",
		`cosign sign --yes "$IMAGE@$IMAGE_DIGEST"`,
		`--certificate-github-workflow-sha "$SOURCE_SHA"`,
	} {
		if !strings.Contains(sign, required) {
			t.Fatalf("new image signing contract is missing %q", required)
		}
	}
	reuse := workflow[reuseStart:manifestStart]
	for _, required := range []string{
		"        if: steps.container-image.outputs.exists == 'true'\n",
		"          EXPECTED_IMAGE_DIGEST: ${{ steps.container-image.outputs.digest }}\n",
		`--certificate-identity "https://github.com/Siyet/worktime/.github/workflows/release.yml@refs/heads/main"`,
		`--certificate-oidc-issuer "https://token.actions.githubusercontent.com"`,
		`--certificate-github-workflow-sha "$SOURCE_SHA"`,
		`"$IMAGE@$EXPECTED_IMAGE_DIGEST"`,
		"          go run ./cmd/ghcr-tag-check\n",
		`echo "IMAGE_DIGEST=$EXPECTED_IMAGE_DIGEST" >> "$GITHUB_ENV"`,
	} {
		if !strings.Contains(reuse, required) {
			t.Fatalf("reusable signed image contract is missing %q", required)
		}
	}
	cosignVerify := strings.Index(reuse, "          cosign verify \\\n")
	metadataVerify := strings.Index(reuse, "          go run ./cmd/ghcr-tag-check\n")
	digestExport := strings.Index(reuse, `echo "IMAGE_DIGEST=$EXPECTED_IMAGE_DIGEST" >> "$GITHUB_ENV"`)
	if cosignVerify < 0 || metadataVerify <= cosignVerify || digestExport <= metadataVerify {
		t.Fatalf("reusable digest must pass signature and registry metadata verification before export: cosign=%d metadata=%d export=%d", cosignVerify, metadataVerify, digestExport)
	}
}

func TestReleaseDownloadsRestoreNativeExecutableModes(t *testing.T) {
	workflow := releaseWorkflow(t)
	downloadedMode := strings.Index(workflow, `chmod 0755 \
            "$RUNNER_TEMP/downloaded/worktime-linux-amd64" \
            "$RUNNER_TEMP/downloaded/worktime-linux-arm64"`)
	downloadedRun := strings.Index(workflow, `"$RUNNER_TEMP/downloaded/worktime-linux-amd64" --version`)
	publishedMode := strings.Index(workflow, `chmod 0755 \
            "$RUNNER_TEMP/published-download/worktime-linux-amd64" \
            "$RUNNER_TEMP/published-download/worktime-linux-arm64"`)
	publishedVerify := strings.LastIndex(workflow, `gh release verify-asset`)
	if downloadedMode < 0 || downloadedRun <= downloadedMode || publishedMode <= downloadedRun || publishedVerify <= publishedMode {
		t.Fatalf("downloaded executable modes are not restored before use/verification: downloaded=%d run=%d published=%d verify=%d", downloadedMode, downloadedRun, publishedMode, publishedVerify)
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

func TestReleaseWaitsOnlyForAsynchronousGitHubAttestation(t *testing.T) {
	workflow := releaseWorkflow(t)
	published := strings.Index(workflow, `gh api --method PATCH "repos/$GITHUB_REPOSITORY/releases/$release_id" -F draft=false`)
	attestationStart := strings.Index(workflow, "          bash .github/scripts/wait-release-attestation.sh 300 10\n")
	immutable := strings.Index(workflow, "if(r.draft || !r.immutable")
	assetVerification := strings.LastIndex(workflow, "gh release verify-asset")
	if published < 0 || attestationStart <= published || immutable <= attestationStart || assetVerification <= immutable {
		t.Fatalf("release attestation polling order is unsafe: publish=%d attestation=%d immutable=%d assets=%d", published, attestationStart, immutable, assetVerification)
	}
	stepStart := strings.LastIndex(workflow[:attestationStart], "      - name: Create, verify, and publish immutable draft")
	if stepStart < 0 || !strings.Contains(workflow[stepStart:attestationStart], "        timeout-minutes: 20\n") {
		t.Fatal("irreversible publish step needs a last-resort timeout")
	}
	disarm := strings.LastIndex(workflow, "cleanup_armed=false")
	if disarm <= assetVerification {
		t.Fatal("release cleanup was disarmed before attestation and asset verification completed")
	}
}

const (
	testReleaseVersion = "v9.8.7"
	testReleaseSHA     = "0123456789abcdef0123456789abcdef01234567"
)

func TestReleaseAttestationWaitAcceptsOnlyExactPendingMessages(t *testing.T) {
	for _, pending := range []string{
		"no attestations for tag " + testReleaseVersion + " (sha1:" + testReleaseSHA + ")",
		"no attestations found for release " + testReleaseVersion + " in worktime",
	} {
		t.Run(pending, func(t *testing.T) {
			directory := t.TempDir()
			writeExecutable(t, filepath.Join(directory, "timeout"), `#!/bin/sh
while test "$#" -gt 0; do
  case "$1" in --signal=*|--kill-after=*) shift ;; *) break ;; esac
done
shift
exec "$@"
`)
			writeExecutable(t, filepath.Join(directory, "gh"), `#!/bin/sh
count=0
test ! -f "$FAKE_GH_COUNT" || count="$(cat "$FAKE_GH_COUNT")"
count=$((count + 1))
printf '%s' "$count" > "$FAKE_GH_COUNT"
if test "$count" -eq 1; then
  printf '%s\n' "$FAKE_GH_PENDING"
  exit 1
fi
printf 'verified\n'
`)
			countPath := filepath.Join(directory, "count")
			command := releaseAttestationCommand(t, directory, "5", "1",
				"FAKE_GH_COUNT="+countPath, "FAKE_GH_PENDING="+pending)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("exact pending response did not retry to success: %v\n%s", err, output)
			}
			if count := readCount(t, countPath); count != 2 {
				t.Fatalf("gh invocation count = %d, want 2", count)
			}
		})
	}
}

func TestReleaseAttestationWaitRejectsCompositeAndUnknownErrors(t *testing.T) {
	pending := "no attestations for tag " + testReleaseVersion + " (sha1:" + testReleaseSHA + ")"
	pendingRelease := "no attestations found for release " + testReleaseVersion + " in worktime"
	for _, output := range []string{
		"prefix " + pending,
		pending + " suffix",
		"HTTP 503\n" + pending,
		"prefix " + pendingRelease,
		pendingRelease + " suffix",
		"HTTP 503\n" + pendingRelease,
		"no attestations for tag v9.8.8 (sha1:" + testReleaseSHA + ")",
		"no attestations for tag " + testReleaseVersion + " (sha1:1123456789abcdef0123456789abcdef01234567)",
		"no attestations found for release v9.8.8 in worktime",
		"no attestations found for release " + testReleaseVersion + " in another-repository",
		"HTTP 401 unauthorized",
		"HTTP 403 forbidden",
		"HTTP 429 rate limited",
		"HTTP 500 server error",
		"network connection reset",
		"no attestation for tag " + testReleaseVersion,
	} {
		t.Run(strings.ReplaceAll(output, "\n", "_"), func(t *testing.T) {
			directory := t.TempDir()
			writeExecutable(t, filepath.Join(directory, "timeout"), `#!/bin/sh
while test "$#" -gt 0; do
  case "$1" in --signal=*|--kill-after=*) shift ;; *) break ;; esac
done
shift
exec "$@"
`)
			writeExecutable(t, filepath.Join(directory, "gh"), `#!/bin/sh
printf '1' > "$FAKE_GH_COUNT"
printf '%s\n' "$FAKE_GH_OUTPUT"
exit 1
`)
			countPath := filepath.Join(directory, "count")
			command := releaseAttestationCommand(t, directory, "3", "1",
				"FAKE_GH_COUNT="+countPath, "FAKE_GH_OUTPUT="+output)
			started := time.Now()
			if command.Run() == nil {
				t.Fatal("unknown/composite verification error was accepted")
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("unknown error was retried for %s", elapsed)
			}
			if count := readCount(t, countPath); count != 1 {
				t.Fatalf("gh invocation count = %d, want 1", count)
			}
		})
	}
}

func TestReleaseAttestationWaitBoundsHungGH(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the release runner uses GNU timeout on Linux")
	}
	directory := t.TempDir()
	writeExecutable(t, filepath.Join(directory, "gh"), `#!/bin/sh
printf 'started' > "$GH_STARTED_MARKER"
trap '' TERM
while :; do sleep 10; done
`)
	helper, err := filepath.Abs("../../.github/scripts/wait-release-attestation.sh")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, "cleanup-state")
	startedMarker := filepath.Join(directory, "gh-started")
	harness := filepath.Join(directory, "workflow-harness")
	writeExecutable(t, harness, `#!/usr/bin/env bash
set -euo pipefail
cleanup_armed=true
cleanup() {
  status=$?
  printf '%s' "$cleanup_armed" > "$CLEANUP_MARKER"
  exit "$status"
}
trap cleanup EXIT
bash "$ATTESTATION_HELPER" 4 1
cleanup_armed=false
`)
	command := exec.Command("bash", harness)
	command.Env = append(os.Environ(),
		"PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"),
		"VERSION="+testReleaseVersion,
		"SOURCE_SHA="+testReleaseSHA,
		"GITHUB_REPOSITORY=Siyet/worktime",
		"ATTESTATION_HELPER="+helper,
		"CLEANUP_MARKER="+marker,
		"GH_STARTED_MARKER="+startedMarker,
	)
	started := time.Now()
	if command.Run() == nil {
		t.Fatal("hung gh unexpectedly succeeded")
	}
	elapsed := time.Since(started)
	if elapsed < 2*time.Second || elapsed > 6*time.Second {
		t.Fatalf("hung gh bounded in %s, want 2s..6s", elapsed)
	}
	if started, err := os.ReadFile(startedMarker); err != nil || string(started) != "started" {
		t.Fatalf("fake gh did not enter the timeout path: marker=%q err=%v", started, err)
	}
	cleanupState, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(cleanupState) != "true" {
		t.Fatalf("cleanup state after bounded failure = %q, want armed", cleanupState)
	}
}

func releaseAttestationCommand(t *testing.T, binDirectory, deadline, retry string, extraEnvironment ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command("bash", "../../.github/scripts/wait-release-attestation.sh", deadline, retry)
	command.Env = append(os.Environ(),
		"PATH="+binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
		"VERSION="+testReleaseVersion,
		"SOURCE_SHA="+testReleaseSHA,
		"GITHUB_REPOSITORY=Siyet/worktime",
	)
	command.Env = append(command.Env, extraEnvironment...)
	return command
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func readCount(t *testing.T, path string) int {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(string(contents))
	if err != nil {
		t.Fatal(err)
	}
	return count
}
