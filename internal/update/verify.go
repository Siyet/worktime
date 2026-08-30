package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
)

const (
	githubActionsIssuer = "https://token.actions.githubusercontent.com"
	releaseWorkflowSAN  = "https://github.com/Siyet/worktime/.github/workflows/release.yml@refs/heads/main"
	tufRepositoryHost   = "tuf-repo-cdn.sigstore.dev"
	tufRequestTimeout   = 20 * time.Second
)

type Verifier interface {
	Verify(context.Context, []byte, []byte) error
}

type SigstoreVerifier struct {
	cacheDirectory string
	httpClient     *http.Client
}

func NewSigstoreVerifier(dataDirectory string) *SigstoreVerifier {
	client := &http.Client{Timeout: 15 * time.Second}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || request.URL.Scheme != "https" || request.URL.Host != tufRepositoryHost {
			return fmt.Errorf("refusing untrusted Sigstore TUF redirect")
		}
		return nil
	}
	return &SigstoreVerifier{
		cacheDirectory: filepath.Join(dataDirectory, "update", "tuf"),
		httpClient:     client,
	}
}

func (v *SigstoreVerifier) Verify(ctx context.Context, manifest, bundleJSON []byte) error {
	var signedBundle bundle.Bundle
	if err := signedBundle.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("decode Sigstore bundle: %w", err)
	}
	verificationContext, cancel := context.WithTimeout(ctx, tufRequestTimeout)
	defer cancel()
	client := v.httpClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	options := tuf.DefaultOptions().
		WithCachePath(v.cacheDirectory).
		WithFetcher(&contextTUFFetcher{ctx: verificationContext, client: client})
	trustedRoot, err := root.FetchTrustedRootWithOptions(options)
	if err != nil {
		return fmt.Errorf("refresh Sigstore trusted root: %w", err)
	}
	digest := sha256.Sum256(manifest)
	return verifySignedBundle(&signedBundle, trustedRoot, "sha256", digest[:], githubActionsIssuer, releaseWorkflowSAN)
}

func verifySignedBundle(signedBundle *bundle.Bundle, trustedRoot *root.TrustedRoot, algorithm string, digest []byte, issuer, san string) error {
	verifier, err := verify.NewVerifier(trustedRoot,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
		verify.WithSignedCertificateTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("construct Sigstore verifier: %w", err)
	}
	identity, err := verify.NewShortCertificateIdentity(issuer, "", san, "")
	if err != nil {
		return fmt.Errorf("construct release identity: %w", err)
	}
	policy := verify.NewPolicy(
		verify.WithArtifactDigest(algorithm, digest),
		verify.WithCertificateIdentity(identity),
	)
	if _, err := verifier.Verify(signedBundle, policy); err != nil {
		return fmt.Errorf("verify release provenance: %w", err)
	}
	return nil
}

type contextTUFFetcher struct {
	ctx    context.Context
	client *http.Client
}

var _ fetcher.Fetcher = (*contextTUFFetcher)(nil)

func (f *contextTUFFetcher) DownloadFile(target string, maximum int64, _ time.Duration) ([]byte, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || parsed.Host != tufRepositoryHost {
		return nil, fmt.Errorf("refusing untrusted Sigstore TUF URL")
	}
	request, err := http.NewRequestWithContext(f.ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "worktime-update-verifier")
	response, err := f.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Sigstore TUF returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, fmt.Errorf("Sigstore TUF response exceeds %d bytes", maximum)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("Sigstore TUF response exceeds %d bytes", maximum)
	}
	return data, nil
}
