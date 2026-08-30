package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
)

const (
	githubActionsIssuer = "https://token.actions.githubusercontent.com"
	releaseWorkflowSAN  = "https://github.com/Siyet/worktime/.github/workflows/release.yml@refs/heads/main"
	tufRepositoryHost   = "tuf-repo-cdn.sigstore.dev"
	tufRequestTimeout   = 20 * time.Second
	// Match go-tuf's default maximum for trusted root metadata. Cached roots
	// are untrusted input until the complete chain has been replay-verified.
	tufRootMaxBytes int64 = 512_000
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
		if len(via) >= 3 || !isTrustedTUFURL(request.URL) {
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
	cachedRoot, err := loadCachedTUFRootChain(v.cacheDirectory)
	if err != nil {
		return fmt.Errorf("validate cached Sigstore root chain: %w", err)
	}
	verificationContext, cancel := context.WithTimeout(ctx, tufRequestTimeout)
	defer cancel()
	client := v.httpClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	downloadedRoots := make(map[int64][]byte)
	options := tuf.DefaultOptions().
		WithRoot(cachedRoot).
		WithCachePath(v.cacheDirectory).
		WithForceCache().
		WithFetcher(&contextTUFFetcher{ctx: verificationContext, client: client, downloadedRoots: downloadedRoots})
	trustedRoot, err := fetchTrustedRoot(options)
	if err != nil {
		return fmt.Errorf("refresh Sigstore trusted root: %w", err)
	}
	if err := persistVerifiedTUFRoots(v.cacheDirectory, downloadedRoots); err != nil {
		return fmt.Errorf("persist verified Sigstore root chain: %w", err)
	}
	digest := sha256.Sum256(manifest)
	return verifySignedBundle(&signedBundle, trustedRoot, "sha256", digest[:], githubActionsIssuer, releaseWorkflowSAN)
}

func fetchTrustedRoot(options *tuf.Options) (trustedRoot *root.TrustedRoot, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("decode cached TUF metadata: %v", recovered)
		}
	}()
	return root.FetchTrustedRootWithOptions(options)
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
	ctx             context.Context
	client          *http.Client
	downloadedRoots map[int64][]byte
}

var _ fetcher.Fetcher = (*contextTUFFetcher)(nil)

func (f *contextTUFFetcher) DownloadFile(target string, maximum int64, _ time.Duration) ([]byte, error) {
	parsed, err := url.Parse(target)
	if err != nil || !isTrustedTUFURL(parsed) {
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
		return nil, &metadata.ErrDownloadHTTP{StatusCode: response.StatusCode, URL: parsed.String()}
	}
	if response.ContentLength > maximum {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: fmt.Sprintf("download failed for %s, length %d is larger than expected %d", parsed.String(), response.ContentLength, maximum)}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: fmt.Sprintf("download failed for %s, length %d is larger than expected %d", parsed.String(), len(data), maximum)}
	}
	if version, ok := tufRootVersion(parsed); ok && f.downloadedRoots != nil {
		f.downloadedRoots[version] = bytes.Clone(data)
	}
	return data, nil
}

func isTrustedTUFURL(value *url.URL) bool {
	return value != nil && value.Scheme == "https" && value.Hostname() == tufRepositoryHost &&
		value.Port() == "" && value.User == nil &&
		value.RawQuery == "" && value.Fragment == ""
}

func tufRootVersion(value *url.URL) (int64, bool) {
	if !isTrustedTUFURL(value) || !strings.HasPrefix(value.EscapedPath(), "/") ||
		!strings.HasSuffix(value.EscapedPath(), ".root.json") {
		return 0, false
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(value.EscapedPath(), "/"), ".root.json")
	if encoded == "" || encoded[0] == '0' {
		return 0, false
	}
	version, err := strconv.ParseInt(encoded, 10, 64)
	return version, err == nil && version > 0 && strconv.FormatInt(version, 10) == encoded
}

func loadCachedTUFRootChain(cacheDirectory string) ([]byte, error) {
	currentRoot := bytes.Clone(tuf.DefaultRoot())
	trustedRoots, err := trustedmetadata.New(currentRoot)
	if err != nil {
		return nil, fmt.Errorf("load embedded root: %w", err)
	}
	rootDirectory := filepath.Join(cacheDirectory, "root-chain")
	for version := trustedRoots.Root.Signed.Version + 1; ; version++ {
		candidate, readErr := readCachedTUFRoot(filepath.Join(rootDirectory, strconv.FormatInt(version, 10)+".root.json"))
		if errors.Is(readErr, os.ErrNotExist) {
			return currentRoot, nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("read root %d: %w", version, readErr)
		}
		if err := updateCachedTUFRoot(trustedRoots, candidate); err != nil {
			return nil, fmt.Errorf("verify root %d: %w", version, err)
		}
		currentRoot = candidate
	}
}

func readCachedTUFRoot(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, tufRootMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > tufRootMaxBytes {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: fmt.Sprintf("cached root %s exceeds maximum length %d", filepath.Base(path), tufRootMaxBytes)}
	}
	return data, nil
}

func updateCachedTUFRoot(trustedRoots *trustedmetadata.TrustedMetadata, candidate []byte) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("decode cached root: %v", recovered)
		}
	}()
	_, err = trustedRoots.UpdateRoot(candidate)
	return err
}

func persistVerifiedTUFRoots(cacheDirectory string, roots map[int64][]byte) error {
	if len(roots) == 0 {
		return nil
	}
	rootDirectory := filepath.Join(cacheDirectory, "root-chain")
	if err := os.MkdirAll(rootDirectory, 0o700); err != nil {
		return err
	}
	versions := make([]int64, 0, len(roots))
	for version := range roots {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(left, right int) bool { return versions[left] < versions[right] })
	for _, version := range versions {
		path := filepath.Join(rootDirectory, strconv.FormatInt(version, 10)+".root.json")
		existing, err := readCachedTUFRoot(path)
		if err == nil {
			if !bytes.Equal(existing, roots[version]) {
				return fmt.Errorf("cached root %d differs from verified root", version)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read cached root %d: %w", version, err)
		}
		if err := writeTUFRootAtomic(path, roots[version]); err != nil {
			return fmt.Errorf("write cached root %d: %w", version, err)
		}
	}
	return nil
}

func writeTUFRootAtomic(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".root-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	_, writeErr := temporary.Write(data)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return syncTUFCacheDirectory(filepath.Dir(path))
}
