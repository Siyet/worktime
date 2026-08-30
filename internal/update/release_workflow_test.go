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

	nativeEnd := strings.Index(workflow[nativeStart:], "      - name: Refuse an existing container image tag")
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
	guardStart := strings.Index(workflow, "      - name: Refuse an existing container image tag")
	imageStart := strings.Index(workflow, "      - name: Build and push multi-platform image")
	signStart := strings.Index(workflow, "      - name: Sign multi-platform image")
	manifestStart := strings.Index(workflow, "      - name: Enforce manifest generation and create signed manifest")
	if guardStart < 0 || imageStart <= guardStart || signStart <= imageStart || manifestStart <= signStart {
		t.Fatalf("container guard/build/sign order is unsafe: guard=%d image=%d sign=%d manifest=%d", guardStart, imageStart, signStart, manifestStart)
	}
	guard := workflow[guardStart:imageStart]
	if !strings.Contains(guard, "          go run ./cmd/ghcr-tag-check\n") {
		t.Fatal("container retry guard must use the status-aware authenticated GHCR checker")
	}
	for _, forbidden := range []string{"imagetools inspect", "grep -Eq", "manifest unknown", "credential"} {
		if strings.Contains(guard, forbidden) {
			t.Fatalf("container retry guard must not classify text or depend on a credential helper: %q", forbidden)
		}
	}
	image := workflow[imageStart:signStart]
	for _, required := range []string{
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
	sign := workflow[signStart:manifestStart]
	if !strings.Contains(sign, `cosign sign --yes "$IMAGE@$IMAGE_DIGEST"`) {
		t.Fatal("image digest must be signed only after the bounded image build succeeds")
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
