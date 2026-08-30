package releaseguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testActor      = "release-actor"
	testToken      = "github-token"
	testRepository = "siyet/worktime"
	testTag        = "v0.1.0"
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
			err := checker.RequireTagAbsent(context.Background(), testActor, testToken, testRepository, testTag)
			if tt.wantExists {
				if !errors.Is(err, ErrTagExists) {
					t.Fatalf("expected existing tag error, got %v", err)
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
			err := checker.RequireTagAbsent(context.Background(), testActor, testToken, testRepository, testTag)
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
	if err := checker.RequireTagAbsent(context.Background(), testActor, testToken, testRepository, testTag); err != nil {
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
		err := checker.RequireTagAbsent(context.Background(), testActor, testToken, testRepository, testTag)
		if err == nil || !strings.Contains(err.Error(), "no such host") {
			t.Fatalf("expected DNS failure, got %v", err)
		}
	})

	t.Run("TLS", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer server.Close()
		checker := GHCRChecker{Client: &http.Client{Timeout: time.Second}, Origin: server.URL}
		err := checker.RequireTagAbsent(context.Background(), testActor, testToken, testRepository, testTag)
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("expected TLS certificate failure, got %v", err)
		}
	})
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
			response.WriteHeader(manifestStatus)
			_, _ = response.Write([]byte(manifestBody))
		default:
			http.NotFound(response, request)
		}
	}))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
