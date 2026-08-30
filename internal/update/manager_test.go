package update

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type acceptingVerifier struct {
	manifest []byte
	bundle   []byte
}

func (v *acceptingVerifier) Verify(_ context.Context, manifest, bundle []byte) error {
	v.manifest = append([]byte(nil), manifest...)
	v.bundle = append([]byte(nil), bundle...)
	return nil
}

type memoryPolicy struct{ enabled bool }

type supportedTestInstaller struct{ applyCalls int }

func (*supportedTestInstaller) Supported() (bool, string) { return true, "" }
func (installer *supportedTestInstaller) Apply(context.Context, Manifest, Asset) error {
	installer.applyCalls++
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testHTTPClient(handler func(*http.Request) (int, []byte)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status, body := handler(request)
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
}

func (p *memoryPolicy) AutoUpdate(context.Context) (bool, error) { return p.enabled, nil }
func (p *memoryPolicy) SetAutoUpdate(_ context.Context, enabled bool) error {
	p.enabled = enabled
	return nil
}

func validManifest(generation uint64, version string) Manifest {
	return Manifest{
		SchemaVersion: 1,
		Generation:    generation,
		Version:       version,
		Revision:      strings.Repeat("c", 40),
		PublishedAt:   time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
		ChangelogURL:  "https://github.com/Siyet/worktime/releases/tag/" + version,
		Image:         Image{Name: "ghcr.io/siyet/worktime", Digest: "sha256:" + strings.Repeat("a", 64)},
		Assets: []Asset{{
			OS: "linux", Arch: "amd64", Name: "worktime-linux-amd64",
			URL:    "https://github.com/Siyet/worktime/releases/download/" + version + "/worktime-linux-amd64",
			SHA256: strings.Repeat("b", 64), Size: 1024,
		}, {
			OS: "linux", Arch: "arm64", Name: "worktime-linux-arm64",
			URL:    "https://github.com/Siyet/worktime/releases/download/" + version + "/worktime-linux-arm64",
			SHA256: strings.Repeat("d", 64), Size: 1024,
		}},
	}
}

func TestManagerChecksSignedManifestAndRejectsReplay(t *testing.T) {
	manifest := validManifest(2, "v1.2.0")
	manifestBytes, _ := json.Marshal(manifest)
	client := testHTTPClient(func(request *http.Request) (int, []byte) {
		if request.URL.Path == "/manifest" {
			return http.StatusOK, manifestBytes
		}
		return http.StatusOK, []byte("{\"bundle\":true}")
	})

	verifier := &acceptingVerifier{}
	manager := NewManager(Options{
		CurrentVersion: "v1.0.0", DataDirectory: t.TempDir(), ChecksEnabled: true,
		Policy: &memoryPolicy{}, Verifier: verifier, HTTPClient: client,
		ManifestURL: "https://updates.test/manifest", BundleURL: "https://updates.test/bundle",
	})
	if err := manager.Check(t.Context()); err != nil {
		t.Fatalf("check: %v", err)
	}
	status := manager.Status(t.Context(), false)
	if status.State != "available" || !status.UpdateAvailable || status.LatestVersion == nil || *status.LatestVersion != "v1.2.0" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if len(verifier.manifest) == 0 || len(verifier.bundle) == 0 {
		t.Fatal("manifest and bundle were not both verified")
	}

	older := validManifest(1, "v1.1.0")
	manifestBytes, _ = json.Marshal(older)
	if err := manager.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("expected replay rejection, got %v", err)
	}
}

func TestManagerRestoresLastVerifiedDisplayStatus(t *testing.T) {
	directory := t.TempDir()
	manifest := validManifest(7, "v2.3.0")
	manifestBytes, _ := json.Marshal(manifest)
	client := testHTTPClient(func(request *http.Request) (int, []byte) {
		if request.URL.Path == "/manifest" {
			return http.StatusOK, manifestBytes
		}
		return http.StatusOK, []byte("{\"bundle\":true}")
	})
	policy := &memoryPolicy{}
	installer := &supportedTestInstaller{}
	manager := NewManager(Options{
		CurrentVersion: "v2.0.0", DataDirectory: directory, ChecksEnabled: true,
		Policy: policy, Verifier: &acceptingVerifier{}, Installer: installer, HTTPClient: client,
		ManifestURL: "https://updates.test/manifest", BundleURL: "https://updates.test/bundle",
	})
	if err := manager.Check(t.Context()); err != nil {
		t.Fatalf("initial check: %v", err)
	}
	if status := manager.Status(t.Context(), false); !status.ApplyReady {
		t.Fatalf("fresh verified update was not apply-ready: %+v", status)
	}

	restarted := NewManager(Options{
		CurrentVersion: "v2.0.0", DataDirectory: directory, ChecksEnabled: false,
		Policy: policy, Installer: installer,
	})
	status := restarted.Status(t.Context(), false)
	if status.State != "available" || !status.UpdateAvailable || status.LatestVersion == nil || *status.LatestVersion != manifest.Version {
		t.Fatalf("persisted status was not restored: %+v", status)
	}
	if status.ApplyReady {
		t.Fatalf("persisted display status authorized apply without a fresh verification: %+v", status)
	}
	if status.AutoApply {
		t.Fatalf("automatic policy changed during restart: %+v", status)
	}
	if err := restarted.SetAutoApply(t.Context(), true); err != nil {
		t.Fatalf("enable automatic policy with display-only metadata: %v", err)
	}
	if status = restarted.Status(t.Context(), false); !status.AutoApply || status.ApplyReady {
		t.Fatalf("automatic policy bypassed fresh verification: %+v", status)
	}
	if status.CheckedAt == nil || *status.CheckedAt <= 0 || status.ChangelogURL == nil || *status.ChangelogURL != manifest.ChangelogURL {
		t.Fatalf("persisted check metadata was not restored: %+v", status)
	}
	if err := restarted.Apply(t.Context()); err == nil {
		t.Fatal("restarted manager applied display-only persisted metadata")
	}
	if installer.applyCalls != 0 {
		t.Fatalf("automatic policy applied unverified cached metadata %d times", installer.applyCalls)
	}
}

func TestManagerCapsManifestBeforeVerification(t *testing.T) {
	client := testHTTPClient(func(*http.Request) (int, []byte) {
		return http.StatusOK, make([]byte, maxManifestBytes+1)
	})
	verifier := &acceptingVerifier{}
	manager := NewManager(Options{
		CurrentVersion: "v1.0.0", DataDirectory: t.TempDir(), ChecksEnabled: true,
		Policy: &memoryPolicy{}, Verifier: verifier, HTTPClient: client,
		ManifestURL: "https://updates.test/manifest", BundleURL: "https://updates.test/bundle",
	})
	if err := manager.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size rejection, got %v", err)
	}
	if verifier.manifest != nil {
		t.Fatal("oversized manifest reached verifier")
	}
}

func TestManagerDoesNoEgressWhenChecksDisabled(t *testing.T) {
	requested := false
	client := testHTTPClient(func(*http.Request) (int, []byte) {
		requested = true
		return http.StatusOK, nil
	})
	manager := NewManager(Options{
		CurrentVersion: "v1.0.0", DataDirectory: filepath.Join(t.TempDir(), "data"),
		ChecksEnabled: false, HTTPClient: client,
		ManifestURL: "https://updates.test/manifest", BundleURL: "https://updates.test/bundle",
	})
	if err := manager.Check(t.Context()); err == nil {
		t.Fatal("disabled check succeeded")
	}
	if requested {
		t.Fatal("disabled check made a network request")
	}
}

func TestNotificationOnlyCannotEnableAutoApply(t *testing.T) {
	policy := &memoryPolicy{}
	manager := NewManager(Options{CurrentVersion: "v1.0.0", DataDirectory: t.TempDir(), Policy: policy})
	if err := manager.SetAutoApply(t.Context(), true); err == nil {
		t.Fatal("notification-only manager enabled automatic updates")
	}
	if policy.enabled {
		t.Fatal("policy changed after rejected request")
	}
}

func TestManifestRejectsUnknownFieldsAndIncompletePlatformSet(t *testing.T) {
	manifest := validManifest(1, "v1.0.0")
	manifest.Assets = manifest.Assets[:1]
	data, _ := json.Marshal(manifest)
	if _, err := decodeManifest(data); err == nil || !strings.Contains(err.Error(), "both Linux assets") {
		t.Fatalf("expected incomplete platform rejection, got %v", err)
	}
	data = append(data[:len(data)-1], []byte(",\"unexpected\":true}")...)
	if _, err := decodeManifest(data); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected closed-schema rejection, got %v", err)
	}
}

func TestManifestRequiresExactReleaseURLsAndAssetNames(t *testing.T) {
	manifest := validManifest(1, "v1.0.0")
	manifest.ChangelogURL = "https://github.com/Siyet/worktime/releases/tag/v1.0.0/notes"
	data, _ := json.Marshal(manifest)
	if _, err := decodeManifest(data); err == nil || !strings.Contains(err.Error(), "changelog URL") {
		t.Fatalf("expected non-exact changelog rejection, got %v", err)
	}

	manifest = validManifest(1, "v1.0.0")
	manifest.Assets[0].Name = "nested-worktime-linux-amd64"
	manifest.Assets[0].URL = "https://github.com/Siyet/worktime/releases/download/v1.0.0/nested-worktime-linux-amd64"
	data, _ = json.Marshal(manifest)
	if _, err := decodeManifest(data); err == nil || !strings.Contains(err.Error(), "assets") {
		t.Fatalf("expected unexpected asset name rejection, got %v", err)
	}
}

func TestSigstoreVerifierRejectsMalformedBundleBeforeTrustRootFetch(t *testing.T) {
	verifier := NewSigstoreVerifier(t.TempDir())
	if err := verifier.Verify(t.Context(), []byte("{}"), []byte("{not-json")); err == nil ||
		!strings.Contains(err.Error(), "decode Sigstore bundle") {
		t.Fatalf("expected bundle parse failure, got %v", err)
	}
}
