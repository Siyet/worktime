package update

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sigstoredata "github.com/sigstore/sigstore-go/pkg/testing/data"
	sigstoretuf "github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/examples/repository/repository"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
)

func TestValidSigstoreBundleAndPolicy(t *testing.T) {
	signedBundle := sigstoredata.Bundle(t, "sigstore.js@2.0.0-provenance.sigstore.json")
	trustedRoot := sigstoredata.TrustedRoot(t, "public-good.json")
	digest, err := hex.DecodeString("46d4e2f74c4877316640000a6fdf8a8b59f1e0847667973e9859f774dd31b8f1e0937813b777fb66a2ac67d50540fe34640966eee9fc2ccca387082b4c85cd3c")
	if err != nil {
		t.Fatalf("decode fixture digest: %v", err)
	}
	if err := verifySignedBundle(
		signedBundle,
		trustedRoot,
		"sha512",
		digest,
		githubActionsIssuer,
		"https://github.com/sigstore/sigstore-js/.github/workflows/release.yml@refs/heads/main",
	); err != nil {
		t.Fatalf("verify valid bundle and exact policy: %v", err)
	}
}

func TestSigstoreTUFFetchHonorsContextCancellation(t *testing.T) {
	signedBundle := sigstoredata.Bundle(t, "sigstore.js@2.0.0-provenance.sigstore.json")
	bundleJSON, err := signedBundle.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal valid bundle: %v", err)
	}
	verifier := NewSigstoreVerifier(t.TempDir())
	verifier.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = verifier.Verify(ctx, []byte("valid manifest bytes"), bundleJSON)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("verification did not return the context deadline: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("verification cancellation took %s", elapsed)
	}
}

func TestSigstoreTUFFetchReturnsTypedHTTPFailures(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fetcher := &contextTUFFetcher{
				ctx: t.Context(),
				client: testHTTPClient(func(*http.Request) (int, []byte) {
					return status, []byte(`{"error":"not available"}`)
				}),
			}
			_, err := fetcher.DownloadFile("https://"+tufRepositoryHost+"/16.root.json", 1024, 0)
			var downloadError *metadata.ErrDownloadHTTP
			if !errors.As(err, &downloadError) || downloadError.StatusCode != status || downloadError.URL != "https://"+tufRepositoryHost+"/16.root.json" {
				t.Fatalf("expected typed HTTP %d failure, got %#v", status, err)
			}
		})
	}
}

func TestSigstoreTUFFetchReturnsTypedLengthFailures(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int64
		body          []byte
	}{
		{name: "advertised length", contentLength: 5, body: []byte("small")},
		{name: "streamed length", contentLength: -1, body: []byte("too large")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					ContentLength: tt.contentLength,
					Body:          io.NopCloser(bytes.NewReader(tt.body)),
					Header:        make(http.Header),
					Request:       request,
				}, nil
			})}
			fetcher := &contextTUFFetcher{ctx: t.Context(), client: client}
			_, err := fetcher.DownloadFile("https://"+tufRepositoryHost+"/timestamp.json", 4, 0)
			var lengthError *metadata.ErrDownloadLengthMismatch
			if !errors.As(err, &lengthError) {
				t.Fatalf("expected typed length failure, got %#v", err)
			}
		})
	}
}

func TestSigstoreTUFFetchRejectsUntrustedURLShapes(t *testing.T) {
	client := testHTTPClient(func(request *http.Request) (int, []byte) {
		t.Fatalf("untrusted URL reached transport: %s", request.URL)
		return 0, nil
	})
	fetcher := &contextTUFFetcher{ctx: t.Context(), client: client}
	for _, target := range []string{
		"http://" + tufRepositoryHost + "/timestamp.json",
		"https://example.com/timestamp.json",
		"https://" + tufRepositoryHost + ":443/timestamp.json",
		"https://user@" + tufRepositoryHost + "/timestamp.json",
		"https://" + tufRepositoryHost + "/timestamp.json?token=secret",
		"https://" + tufRepositoryHost + "/timestamp.json#fragment",
	} {
		t.Run(target, func(t *testing.T) {
			if _, err := fetcher.DownloadFile(target, 1024, 0); err == nil || !strings.Contains(err.Error(), "refusing untrusted") {
				t.Fatalf("expected untrusted TUF URL rejection, got %v", err)
			}
		})
	}
}

func TestSigstoreTUFRedirectPolicyStaysOnExactHost(t *testing.T) {
	verifier := NewSigstoreVerifier(t.TempDir())
	source, err := http.NewRequest(http.MethodGet, "https://"+tufRepositoryHost+"/timestamp.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		target  string
		via     []*http.Request
		allowed bool
	}{
		{name: "same host", target: "https://" + tufRepositoryHost + "/snapshot.json", via: []*http.Request{source}, allowed: true},
		{name: "HTTP", target: "http://" + tufRepositoryHost + "/snapshot.json", via: []*http.Request{source}},
		{name: "other host", target: "https://example.com/snapshot.json", via: []*http.Request{source}},
		{name: "port", target: "https://" + tufRepositoryHost + ":443/snapshot.json", via: []*http.Request{source}},
		{name: "userinfo", target: "https://user@" + tufRepositoryHost + "/snapshot.json", via: []*http.Request{source}},
		{name: "query", target: "https://" + tufRepositoryHost + "/snapshot.json?token=secret", via: []*http.Request{source}},
		{name: "fragment", target: "https://" + tufRepositoryHost + "/snapshot.json#fragment", via: []*http.Request{source}},
		{name: "too many hops", target: "https://" + tufRepositoryHost + "/snapshot.json", via: []*http.Request{source, source, source}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			target, requestErr := http.NewRequest(http.MethodGet, tt.target, nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			err := verifier.httpClient.CheckRedirect(target, tt.via)
			if tt.allowed && err != nil {
				t.Fatalf("trusted redirect rejected: %v", err)
			}
			if !tt.allowed && err == nil {
				t.Fatal("untrusted redirect accepted")
			}
		})
	}
}

func TestTUFRootVersionAcceptsOnlyCanonicalRootPaths(t *testing.T) {
	for _, tt := range []struct {
		target  string
		version int64
		valid   bool
	}{
		{target: "https://" + tufRepositoryHost + "/14.root.json", version: 14, valid: true},
		{target: "https://" + tufRepositoryHost + "/0.root.json"},
		{target: "https://" + tufRepositoryHost + "/014.root.json"},
		{target: "https://" + tufRepositoryHost + "/14.root.json/extra"},
		{target: "https://" + tufRepositoryHost + "/metadata/14.root.json"},
		{target: "https://example.com/14.root.json"},
		{target: "https://" + tufRepositoryHost + "/14.root.json?token=secret"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			parsed, err := url.Parse(tt.target)
			if err != nil {
				t.Fatal(err)
			}
			version, valid := tufRootVersion(parsed)
			if valid != tt.valid || version != tt.version {
				t.Fatalf("root version = (%d, %v), want (%d, %v)", version, valid, tt.version, tt.valid)
			}
		})
	}
}

func TestCachedTUFRootChainRejectsCorruptNextRoot(t *testing.T) {
	embedded, err := metadata.Root().FromBytes(sigstoretuf.DefaultRoot())
	if err != nil {
		t.Fatal(err)
	}
	cacheDirectory := t.TempDir()
	rootDirectory := filepath.Join(cacheDirectory, "root-chain")
	if err := os.MkdirAll(rootDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	nextVersion := embedded.Signed.Version + 1
	if err := os.WriteFile(filepath.Join(rootDirectory, strconv.FormatInt(nextVersion, 10)+".root.json"), []byte(`{"corrupt":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCachedTUFRootChain(cacheDirectory); err == nil || !strings.Contains(err.Error(), "verify root") {
		t.Fatalf("corrupt cached root chain must fail closed, got %v", err)
	}
}

func TestCachedTUFRootChainRejectsOversizedNextRoot(t *testing.T) {
	embedded, err := metadata.Root().FromBytes(sigstoretuf.DefaultRoot())
	if err != nil {
		t.Fatal(err)
	}
	cacheDirectory := t.TempDir()
	rootDirectory := filepath.Join(cacheDirectory, "root-chain")
	if err := os.MkdirAll(rootDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	nextVersion := embedded.Signed.Version + 1
	path := filepath.Join(rootDirectory, strconv.FormatInt(nextVersion, 10)+".root.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), int(tufRootMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadCachedTUFRootChain(cacheDirectory)
	var lengthError *metadata.ErrDownloadLengthMismatch
	if !errors.As(err, &lengthError) {
		t.Fatalf("oversized cached root must return a typed length error, got %v", err)
	}
	if !strings.Contains(err.Error(), strconv.FormatInt(tufRootMaxBytes, 10)) || !strings.Contains(err.Error(), strconv.FormatInt(nextVersion, 10)+".root.json") {
		t.Fatalf("oversized cached root error must identify the cap and root, got %v", err)
	}
}

func TestAuthenticExpiredTUFCacheFailsClosedWithoutNetworkEgress(t *testing.T) {
	repositoryFixture := newAuthenticTUFRepository(t)
	cacheDirectory := t.TempDir()
	options := sigstoretuf.DefaultOptions().
		WithRoot(repositoryFixture.rootBytes(t)).
		WithCachePath(cacheDirectory).
		WithRepositoryBaseURL(repositoryFixture.baseURL).
		WithFetcher(repositoryFixture).
		WithForceCache()
	if _, err := sigstoretuf.New(options); err != nil {
		t.Fatalf("populate authentic signed TUF cache: %v", err)
	}

	repositoryFixture.roles.Timestamp().Signed.Expires = time.Now().UTC().Add(-time.Hour)
	repositoryFixture.signRole(t, metadata.TIMESTAMP)
	expiredTimestamp, err := repositoryFixture.roles.Timestamp().ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	trustedSet, err := trustedmetadata.New(repositoryFixture.rootBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = trustedSet.UpdateTimestamp(expiredTimestamp)
	var expiredError *metadata.ErrExpiredMetadata
	if !errors.As(err, &expiredError) {
		t.Fatalf("signed timestamp fixture must authenticate and fail specifically on expiry, got %v", err)
	}
	timestampPath := filepath.Join(cacheDirectory, sigstoretuf.URLToPath(repositoryFixture.baseURL), "timestamp.json")
	if err := os.WriteFile(timestampPath, expiredTimestamp, 0o600); err != nil {
		t.Fatal(err)
	}

	offline := &offlineTUFFetcher{}
	offlineOptions := sigstoretuf.DefaultOptions().
		WithRoot(repositoryFixture.rootBytes(t)).
		WithCachePath(cacheDirectory).
		WithRepositoryBaseURL(repositoryFixture.baseURL).
		WithFetcher(offline).
		WithForceCache()
	if _, err := sigstoretuf.New(offlineOptions); err == nil {
		t.Fatalf("authentic signed expired cache must fail closed, got %v", err)
	}
	if offline.calls == 0 {
		t.Fatal("expired cache did not attempt a refresh through the injected offline fetcher")
	}
}

type authenticTUFRepository struct {
	baseURL string
	keys    map[string]ed25519.PrivateKey
	roles   *repository.Type
}

func newAuthenticTUFRepository(t *testing.T) *authenticTUFRepository {
	t.Helper()
	fixture := &authenticTUFRepository{
		baseURL: "https://tuf.testing.invalid",
		keys:    make(map[string]ed25519.PrivateKey),
		roles:   repository.New(),
	}
	expires := time.Now().UTC().Add(24 * time.Hour)
	fixture.roles.SetRoot(metadata.Root(expires))
	fixture.roles.SetTargets(metadata.TARGETS, metadata.Targets(expires))
	fixture.roles.SetSnapshot(metadata.Snapshot(expires))
	fixture.roles.SetTimestamp(metadata.Timestamp(expires))
	fixture.roles.Snapshot().Signed.Meta["targets.json"] = metadata.MetaFile(fixture.roles.Targets(metadata.TARGETS).Signed.Version)
	fixture.roles.Timestamp().Signed.Meta["snapshot.json"] = metadata.MetaFile(fixture.roles.Snapshot().Signed.Version)

	for _, role := range metadata.TOP_LEVEL_ROLE_NAMES {
		_, privateKey, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		fixture.keys[role] = privateKey
		publicKey, err := metadata.KeyFromPublicKey(privateKey.Public())
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.roles.Root().Signed.AddKey(publicKey, role); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range metadata.TOP_LEVEL_ROLE_NAMES {
		fixture.signRole(t, role)
	}
	return fixture
}

func (fixture *authenticTUFRepository) signRole(t *testing.T, role string) {
	t.Helper()
	signer, err := signature.LoadSigner(fixture.keys[role], crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	switch role {
	case metadata.ROOT:
		fixture.roles.Root().ClearSignatures()
		_, err = fixture.roles.Root().Sign(signer)
	case metadata.TARGETS:
		fixture.roles.Targets(metadata.TARGETS).ClearSignatures()
		_, err = fixture.roles.Targets(metadata.TARGETS).Sign(signer)
	case metadata.SNAPSHOT:
		fixture.roles.Snapshot().ClearSignatures()
		_, err = fixture.roles.Snapshot().Sign(signer)
	case metadata.TIMESTAMP:
		fixture.roles.Timestamp().ClearSignatures()
		_, err = fixture.roles.Timestamp().Sign(signer)
	default:
		t.Fatalf("unsupported TUF role %q", role)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func (fixture *authenticTUFRepository) rootBytes(t *testing.T) []byte {
	t.Helper()
	data, err := fixture.roles.Root().ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (fixture *authenticTUFRepository) DownloadFile(target string, _ int64, _ time.Duration) ([]byte, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	switch parsed.Path {
	case "/timestamp.json":
		return fixture.roles.Timestamp().ToBytes(false)
	case fmt.Sprintf("/%d.snapshot.json", fixture.roles.Snapshot().Signed.Version):
		return fixture.roles.Snapshot().ToBytes(false)
	case fmt.Sprintf("/%d.targets.json", fixture.roles.Targets(metadata.TARGETS).Signed.Version):
		return fixture.roles.Targets(metadata.TARGETS).ToBytes(false)
	default:
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusNotFound, URL: target}
	}
}

type offlineTUFFetcher struct {
	calls int
}

func (fetcher *offlineTUFFetcher) DownloadFile(string, int64, time.Duration) ([]byte, error) {
	fetcher.calls++
	return nil, errors.New("network is disabled for this test")
}

func TestPersistVerifiedTUFRootsRejectsExistingMismatch(t *testing.T) {
	cacheDirectory := t.TempDir()
	if err := persistVerifiedTUFRoots(cacheDirectory, map[int64][]byte{14: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	if err := persistVerifiedTUFRoots(cacheDirectory, map[int64][]byte{14: []byte("different")}); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("cached root replacement must fail closed, got %v", err)
	}
}

func TestSigstorePublicGoodReleaseIntegrationAndOfflineCache(t *testing.T) {
	if os.Getenv("WORKTIME_SIGSTORE_INTEGRATION") != "1" {
		t.Skip("set WORKTIME_SIGSTORE_INTEGRATION=1 to verify the immutable public release against live Sigstore TUF")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	manifest := downloadPublicReleaseFixture(t, ctx, "release-manifest.json", maxManifestBytes)
	bundleJSON := downloadPublicReleaseFixture(t, ctx, "release-manifest.sigstore.json", maxBundleBytes)

	dataDirectory := t.TempDir()
	verifier := NewSigstoreVerifier(dataDirectory)
	recorder := &recordingRoundTripper{base: http.DefaultTransport}
	verifier.httpClient.Transport = recorder
	if err := verifier.Verify(ctx, manifest, bundleJSON); err != nil {
		t.Fatalf("verify immutable v0.1.1 with live public-good TUF: %v", err)
	}
	requested := strings.Join(recorder.paths, "\n")
	for _, expected := range []string{"/16.root.json", "/timestamp.json", ".snapshot.json", "/targets/"} {
		if !strings.Contains(requested, expected) {
			t.Fatalf("live TUF refresh did not request %q; paths:\n%s", expected, requested)
		}
	}

	offlineCalls := 0
	verifier.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		offlineCalls++
		return nil, errors.New("network is offline")
	})
	if err := verifier.Verify(ctx, manifest, bundleJSON); err != nil {
		t.Fatalf("verify from unexpired signed TUF cache while offline: %v", err)
	}
	if offlineCalls != 0 {
		t.Fatalf("offline cached verification made %d network requests", offlineCalls)
	}

	timestampPath := filepath.Join(dataDirectory, "update", "tuf", tufRepositoryHost, "timestamp.json")
	if err := os.WriteFile(timestampPath, []byte(`{"corrupt":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(ctx, manifest, bundleJSON); err == nil {
		t.Fatal("corrupt cached TUF metadata must fail closed while offline")
	}

	missingCacheVerifier := NewSigstoreVerifier(t.TempDir())
	missingCacheVerifier.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network is offline")
	})
	if err := missingCacheVerifier.Verify(ctx, manifest, bundleJSON); err == nil {
		t.Fatal("missing TUF cache must fail closed while offline")
	}
}

type recordingRoundTripper struct {
	base  http.RoundTripper
	paths []string
}

func (transport *recordingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.paths = append(transport.paths, request.URL.EscapedPath())
	return transport.base.RoundTrip(request)
}

func downloadPublicReleaseFixture(t *testing.T, ctx context.Context, name string, maximum int64) []byte {
	t.Helper()
	fixtureURL := "https://github.com/Siyet/worktime/releases/download/v0.1.1/" + name
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fixtureURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("download immutable v0.1.1 fixture %s: %v", name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("download immutable v0.1.1 fixture %s: HTTP %d", name, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > maximum {
		t.Fatalf("immutable v0.1.1 fixture %s exceeds %d bytes", name, maximum)
	}
	return data
}
