package update

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	sigstoredata "github.com/sigstore/sigstore-go/pkg/testing/data"
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
