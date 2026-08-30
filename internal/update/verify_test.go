package update

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sigstoredata "github.com/sigstore/sigstore-go/pkg/testing/data"
	"github.com/theupdateframework/go-tuf/v2/metadata"
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
