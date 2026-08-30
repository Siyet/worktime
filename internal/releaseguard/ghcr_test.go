package releaseguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testActor      = "release-actor"
	testToken      = "github-token"
	testRepository = "siyet/worktime"
	testTag        = "v0.1.0"
	testRevision   = "0123456789abcdef0123456789abcdef01234567"
)

func TestGHCRCheckerClassifiesOnlyExactMissingManifestAsAbsent(t *testing.T) {
	t.Setenv("PATH", "") // The lookup must not depend on Docker or a credential helper.
	tests := []struct {
		name           string
		manifestStatus int
		manifestBody   string
		wantError      string
		wantExists     bool
	}{
		{name: "exact missing", manifestStatus: http.StatusNotFound, manifestBody: `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`},
		{name: "existing", manifestStatus: http.StatusOK, manifestBody: `{}`, wantExists: true},
		{name: "unauthorized", manifestStatus: http.StatusUnauthorized, manifestBody: `{}`, wantError: "unexpected HTTP 401"},
		{name: "forbidden", manifestStatus: http.StatusForbidden, manifestBody: `{}`, wantError: "unexpected HTTP 403"},
		{name: "wrong 404", manifestStatus: http.StatusNotFound, manifestBody: `{"errors":[{"code":"NAME_UNKNOWN"}]}`, wantError: "without exact MANIFEST_UNKNOWN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newRegistryServer(t, http.StatusOK, `{"token":"registry-token"}`, tt.manifestStatus, tt.manifestBody)
			defer server.Close()
			checker := GHCRChecker{Client: server.Client(), Origin: server.URL}
			resolution, err := checker.ResolveTag(context.Background(), testActor, testToken, testRepository, testTag)
			if tt.wantExists {
				if err != nil || !resolution.Exists || !digestRE.MatchString(resolution.Digest) {
					t.Fatalf("expected existing tag resolution, got %+v, %v", resolution, err)
				}
				return
			}
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("expected exact missing manifest to be accepted, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
			}
		})
	}
}

func TestGHCRCheckerRejectsTokenEndpointFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := newRegistryServer(t, status, `{}`, http.StatusNotFound, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`)
			defer server.Close()
			checker := GHCRChecker{Client: server.Client(), Origin: server.URL}
			_, err := checker.ResolveTag(context.Background(), testActor, testToken, testRepository, testTag)
			want := fmt.Sprintf("unexpected HTTP %d", status)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected token endpoint error containing %q, got %v", want, err)
			}
		})
	}
}

func TestGHCRCheckerDoesNotUseCredentialHelpers(t *testing.T) {
	t.Setenv("PATH", "/missing-credential-helpers")
	server := newRegistryServer(t, http.StatusOK, `{"token":"registry-token"}`, http.StatusNotFound, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`)
	defer server.Close()
	checker := GHCRChecker{Client: server.Client(), Origin: server.URL}
	if _, err := checker.ResolveTag(context.Background(), testActor, testToken, testRepository, testTag); err != nil {
		t.Fatalf("status-aware HTTP lookup must not require a Docker credential helper: %v", err)
	}
}

func TestGHCRCheckerFailsClosedOnDNSAndTLSFailures(t *testing.T) {
	t.Run("DNS", func(t *testing.T) {
		checker := GHCRChecker{
			Origin: "https://ghcr.io",
			Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, &net.DNSError{Err: "no such host", Name: "ghcr.io", IsNotFound: true}
			})},
		}
		_, err := checker.ResolveTag(context.Background(), testActor, testToken, testRepository, testTag)
		if err == nil || !strings.Contains(err.Error(), "HTTP transport failed") {
			t.Fatalf("expected DNS failure, got %v", err)
		}
	})

	t.Run("TLS", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer server.Close()
		checker := GHCRChecker{Client: &http.Client{Timeout: time.Second}, Origin: server.URL}
		_, err := checker.ResolveTag(context.Background(), testActor, testToken, testRepository, testTag)
		if err == nil || !strings.Contains(err.Error(), "HTTP transport failed") {
			t.Fatalf("expected TLS certificate failure, got %v", err)
		}
	})
}

func TestGHCRBlobRedirectAllowsOnlyDigestMatchedGitHubContainerDelivery(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		wantError string
	}{
		{name: "valid blob", body: []byte("verified image config")},
		{name: "digest mismatch", body: []byte("substituted image config"), wantError: "digest mismatch"},
		{name: "response cap", body: bytes.Repeat([]byte("x"), maxRegistryResponseBytes+1), wantError: "response exceeds size limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectedBody := []byte("verified image config")
			digest := testDigest(expectedBody)
			origin, err := url.Parse("https://ghcr.io")
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			client := &http.Client{
				CheckRedirect: CheckGHCRBlobRedirect,
				Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					requests++
					switch request.URL.Hostname() {
					case "ghcr.io":
						if got := request.Header.Get("Authorization"); got != "Bearer registry-token" {
							t.Fatalf("registry request authorization = %q", got)
						}
						return redirectResponse(request, "https://pkg-containers.githubusercontent.com/ghcr1/blobs/"+digest+"?sig=secret&hmac=capability"), nil
					case "pkg-containers.githubusercontent.com":
						if got := request.Header.Get("Authorization"); got != "" {
							t.Fatalf("cross-host blob request leaked authorization %q", got)
						}
						return responseWithBody(request, http.StatusOK, tt.body), nil
					default:
						t.Fatalf("unexpected redirect host %q", request.URL.Host)
						return nil, nil
					}
				}),
			}
			checker := GHCRChecker{Client: client, Origin: origin.String()}
			body, err := checker.fetchBlob(context.Background(), origin, "registry-token", testRepository, digest)
			if tt.wantError == "" {
				if err != nil || !bytes.Equal(body, expectedBody) {
					t.Fatalf("fetch redirected blob: body=%q err=%v", body, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
			}
			if requests != 2 {
				t.Fatalf("redirected blob request count = %d, want 2", requests)
			}
		})
	}
}

func TestGHCRBlobRedirectRejectsUnsafeTargetsAndExtraHops(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	source, err := http.NewRequest(http.MethodGet, "https://ghcr.io/v2/"+testRepository+"/blobs/"+digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		target string
		via    []*http.Request
	}{
		{name: "HTTP downgrade", target: "http://pkg-containers.githubusercontent.com/ghcr1/blobs/" + digest, via: []*http.Request{source}},
		{name: "arbitrary host", target: "https://example.com/ghcr1/blobs/" + digest, via: []*http.Request{source}},
		{name: "wrong path", target: "https://pkg-containers.githubusercontent.com/files/" + digest, via: []*http.Request{source}},
		{name: "wrong digest", target: "https://pkg-containers.githubusercontent.com/ghcr1/blobs/sha256:" + strings.Repeat("b", 64), via: []*http.Request{source}},
		{name: "multiple hops", target: "https://pkg-containers.githubusercontent.com/ghcr1/blobs/" + digest, via: []*http.Request{source, source}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, requestErr := http.NewRequest(http.MethodGet, tt.target, nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			target.Header.Set("Authorization", "Bearer must-not-leak")
			if err := CheckGHCRBlobRedirect(target, tt.via); err == nil {
				t.Fatal("expected unsafe redirect to be rejected")
			}
		})
	}
}

func TestGHCRBlobRedirectStripsCrossHostCredentials(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	source, err := http.NewRequest(http.MethodGet, "https://ghcr.io/v2/"+testRepository+"/blobs/"+digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := http.NewRequest(http.MethodGet, "https://pkg-containers.githubusercontent.com/ghcr1/blobs/"+digest+"?sig=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	target.Header.Set("Authorization", "Bearer registry-token")
	target.Header.Set("Proxy-Authorization", "Basic proxy-token")
	if err := CheckGHCRBlobRedirect(target, []*http.Request{source}); err != nil {
		t.Fatalf("accept real-shape GitHub container redirect: %v", err)
	}
	if target.Header.Get("Authorization") != "" || target.Header.Get("Proxy-Authorization") != "" {
		t.Fatalf("cross-host credentials were not stripped: %#v", target.Header)
	}
}

func TestTransportErrorsNeverExposeCapabilityURLs(t *testing.T) {
	secretURL := "https://user:password@pkg-containers.githubusercontent.com/ghcr1/blobs/sha256:" + strings.Repeat("a", 64) + "?sig=secret&hmac=capability#private"
	err := sanitizeTransportError(&url.Error{Op: "Get", URL: secretURL, Err: errors.New("redirect failed for " + secretURL)})
	message := err.Error()
	for _, forbidden := range []string{"user:password@", "password", "sig=", "secret", "hmac=", "capability", "?", "#private"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("sanitized transport error leaked %q: %s", forbidden, message)
		}
	}
	if !strings.Contains(message, "https://pkg-containers.githubusercontent.com/ghcr1/blobs/sha256:") {
		t.Fatalf("sanitized transport error lost its safe endpoint context: %s", message)
	}
}

func TestGHCRCheckerVerifiesReusableImageMetadataAndStableDigest(t *testing.T) {
	tests := []struct {
		name          string
		versionLabel  string
		revisionLabel string
		mutateTag     bool
		wantError     string
	}{
		{name: "matching signed-build metadata", versionLabel: testTag, revisionLabel: testRevision},
		{name: "wrong version", versionLabel: "v0.2.0", revisionLabel: testRevision, wantError: "wrong version label"},
		{name: "wrong revision", versionLabel: testTag, revisionLabel: "other-sha", wantError: "wrong revision label"},
		{name: "tag changes during verification", versionLabel: testTag, revisionLabel: testRevision, mutateTag: true, wantError: "digest changed during verification"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, digest := newReusableRegistryServer(t, tt.versionLabel, tt.revisionLabel, tt.mutateTag)
			defer server.Close()
			checker := GHCRChecker{Client: server.Client(), Origin: server.URL}
			resolution, err := checker.ResolveTag(context.Background(), testActor, testToken, testRepository, testTag)
			if err != nil || !resolution.Exists || resolution.Digest != digest {
				t.Fatalf("resolve reusable tag: %+v, %v", resolution, err)
			}
			err = checker.VerifyReusableTag(context.Background(), testActor, testToken, testRepository, testTag, digest, testRevision)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("verify reusable image: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
			}
		})
	}
}

func TestGHCRCheckerRejectsInvalidExpectedRevisionBeforeRegistryAccess(t *testing.T) {
	checker := GHCRChecker{Client: &http.Client{}, Origin: "https://ghcr.io"}
	err := checker.VerifyReusableTag(context.Background(), testActor, testToken, testRepository, testTag, "sha256:"+strings.Repeat("0", 64), "")
	if err == nil || !strings.Contains(err.Error(), "invalid expected source revision") {
		t.Fatalf("expected invalid source revision failure, got %v", err)
	}
}

func TestGHCRCheckerRejectsMalformedExistingManifestDigest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			_, _ = response.Write([]byte(`{"token":"registry-token"}`))
			return
		}
		response.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("0", 64))
		_, _ = response.Write([]byte(`{"schemaVersion":2}`))
	}))
	defer server.Close()
	checker := GHCRChecker{Client: server.Client(), Origin: server.URL}
	_, err := checker.ResolveTag(context.Background(), testActor, testToken, testRepository, testTag)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected malformed manifest digest failure, got %v", err)
	}
}

func newRegistryServer(t *testing.T, tokenStatus int, tokenBody string, manifestStatus int, manifestBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			actor, token, ok := request.BasicAuth()
			if !ok || actor != testActor || token != testToken {
				t.Errorf("token request did not use the expected basic authentication")
			}
			if got := request.URL.Query().Get("service"); got != "ghcr.io" {
				t.Errorf("token service = %q, want ghcr.io", got)
			}
			if got := request.URL.Query().Get("scope"); got != "repository:"+testRepository+":pull" {
				t.Errorf("token scope = %q", got)
			}
			response.WriteHeader(tokenStatus)
			_, _ = response.Write([]byte(tokenBody))
		case "/v2/" + testRepository + "/manifests/" + testTag:
			if got := request.Header.Get("Authorization"); got != "Bearer registry-token" {
				t.Errorf("manifest authorization = %q", got)
			}
			if !strings.Contains(request.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				t.Errorf("manifest request is missing OCI index media type")
			}
			if manifestStatus == http.StatusOK {
				response.Header().Set("Docker-Content-Digest", testDigest([]byte(manifestBody)))
			}
			response.WriteHeader(manifestStatus)
			_, _ = response.Write([]byte(manifestBody))
		default:
			http.NotFound(response, request)
		}
	}))
}

func newReusableRegistryServer(t *testing.T, versionLabel, revisionLabel string, mutateTag bool) (*httptest.Server, string) {
	t.Helper()
	configBodies := map[string][]byte{}
	manifestBodies := map[string][]byte{}
	descriptors := make([]map[string]any, 0, 2)
	for _, architecture := range []string{"amd64", "arm64"} {
		configBody, err := json.Marshal(map[string]any{
			"architecture": architecture,
			"os":           "linux",
			"config": map[string]any{"Labels": map[string]string{
				"org.opencontainers.image.version":  versionLabel,
				"org.opencontainers.image.revision": revisionLabel,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		configDigest := testDigest(configBody)
		configBodies[configDigest] = configBody
		manifestBody, err := json.Marshal(map[string]any{
			"schemaVersion": 2,
			"mediaType":     "application/vnd.oci.image.manifest.v1+json",
			"config": map[string]string{
				"mediaType": "application/vnd.oci.image.config.v1+json",
				"digest":    configDigest,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		manifestDigest := testDigest(manifestBody)
		manifestBodies[manifestDigest] = manifestBody
		descriptors = append(descriptors, map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    manifestDigest,
			"platform":  map[string]string{"os": "linux", "architecture": architecture},
		})
	}
	indexBody, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     descriptors,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexDigest := testDigest(indexBody)
	tagLookups := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/token":
			_, _ = response.Write([]byte(`{"token":"registry-token"}`))
		case request.URL.Path == "/v2/"+testRepository+"/manifests/"+testTag:
			tagLookups++
			body := indexBody
			if mutateTag && tagLookups > 1 {
				body = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
			}
			response.Header().Set("Docker-Content-Digest", testDigest(body))
			_, _ = response.Write(body)
		case strings.HasPrefix(request.URL.Path, "/v2/"+testRepository+"/manifests/"):
			reference := strings.TrimPrefix(request.URL.Path, "/v2/"+testRepository+"/manifests/")
			body := indexBody
			if reference != indexDigest {
				body = manifestBodies[reference]
			}
			if body == nil {
				http.NotFound(response, request)
				return
			}
			response.Header().Set("Docker-Content-Digest", testDigest(body))
			_, _ = response.Write(body)
		case strings.HasPrefix(request.URL.Path, "/v2/"+testRepository+"/blobs/"):
			reference := strings.TrimPrefix(request.URL.Path, "/v2/"+testRepository+"/blobs/")
			body := configBodies[reference]
			if body == nil {
				http.NotFound(response, request)
				return
			}
			_, _ = response.Write(body)
		default:
			http.NotFound(response, request)
		}
	}))
	return server, indexDigest
}

func testDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

func redirectResponse(request *http.Request, location string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusTemporaryRedirect,
		Header:     http.Header{"Location": []string{location}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}
}

func responseWithBody(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
