package releaseguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const maxRegistryResponseBytes = 1 << 20

var (
	versionTag             = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	digestRE               = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	revisionRE             = regexp.MustCompile(`^[0-9a-f]{40}$`)
	ghcrBlobRedirectPathRE = regexp.MustCompile(`^/ghcrblobs0*[1-9][0-9]*/blobs/(sha256:[0-9a-f]{64})$`)
)

type TagResolution struct {
	Exists bool
	Digest string
}

type GHCRChecker struct {
	Client *http.Client
	Origin string
}

// CheckGHCRBlobRedirect accepts the one cross-host redirect GHCR uses for blob
// delivery. The signed query is a capability, so redirects are deliberately
// constrained and registry credentials are removed before following it.
func CheckGHCRBlobRedirect(request *http.Request, via []*http.Request) error {
	if len(via) != 1 {
		return errors.New("registry blob redirect must have exactly one hop")
	}
	source := via[0].URL
	target := request.URL
	if source.Scheme != "https" || source.Hostname() != "ghcr.io" || (source.Port() != "" && source.Port() != "443") {
		return errors.New("registry blob redirect has an invalid source")
	}
	sourceDigest, ok := blobDigestFromPath(source.Path, "/v2/")
	if !ok {
		return errors.New("registry blob redirect source is not a digest-addressed blob")
	}
	if target.Scheme != "https" || target.Hostname() != "pkg-containers.githubusercontent.com" || (target.Port() != "" && target.Port() != "443") || target.User != nil || target.Fragment != "" {
		return errors.New("registry blob redirect has an invalid target")
	}
	targetMatch := ghcrBlobRedirectPathRE.FindStringSubmatch(target.Path)
	if len(targetMatch) != 2 || targetMatch[1] != sourceDigest {
		return errors.New("registry blob redirect target does not match the requested digest")
	}
	request.Header.Del("Authorization")
	request.Header.Del("Proxy-Authorization")
	return nil
}

func blobDigestFromPath(value, prefix string) (string, bool) {
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	marker := "/blobs/"
	index := strings.LastIndex(value, marker)
	if index < len(prefix) || strings.Contains(value[index+len(marker):], "/") {
		return "", false
	}
	digest := value[index+len(marker):]
	return digest, digestRE.MatchString(digest)
}

func (c GHCRChecker) ResolveTag(ctx context.Context, actor, token, repository, tag string) (TagResolution, error) {
	origin, err := c.validate(actor, token, repository, tag)
	if err != nil {
		return TagResolution{}, err
	}
	registryToken, err := c.fetchToken(ctx, origin, actor, token, repository)
	if err != nil {
		return TagResolution{}, err
	}
	body, digest, status, err := c.fetchManifest(ctx, origin, registryToken, repository, tag)
	if err != nil {
		return TagResolution{}, err
	}
	if status == http.StatusOK {
		if err := verifyDigest(body, digest); err != nil {
			return TagResolution{}, fmt.Errorf("verify tagged registry manifest: %w", err)
		}
		return TagResolution{Exists: true, Digest: digest}, nil
	}
	if status != http.StatusNotFound {
		return TagResolution{}, fmt.Errorf("request registry manifest: unexpected HTTP %d", status)
	}
	var payload struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return TagResolution{}, fmt.Errorf("decode missing registry manifest: %w", err)
	}
	if len(payload.Errors) != 1 || payload.Errors[0].Code != "MANIFEST_UNKNOWN" {
		return TagResolution{}, errors.New("registry returned HTTP 404 without exact MANIFEST_UNKNOWN status")
	}
	return TagResolution{}, nil
}

func (c GHCRChecker) VerifyReusableTag(ctx context.Context, actor, token, repository, tag, expectedDigest, expectedRevision string) error {
	if !digestRE.MatchString(expectedDigest) {
		return errors.New("invalid expected image digest")
	}
	if !revisionRE.MatchString(expectedRevision) {
		return errors.New("invalid expected source revision")
	}
	origin, err := c.validate(actor, token, repository, tag)
	if err != nil {
		return err
	}
	registryToken, err := c.fetchToken(ctx, origin, actor, token, repository)
	if err != nil {
		return err
	}
	if err := c.verifyImageMetadata(ctx, origin, registryToken, repository, expectedDigest, tag, expectedRevision); err != nil {
		return err
	}
	resolution, err := c.ResolveTag(ctx, actor, token, repository, tag)
	if err != nil {
		return fmt.Errorf("re-resolve container tag after verification: %w", err)
	}
	if !resolution.Exists || resolution.Digest != expectedDigest {
		return fmt.Errorf("container tag digest changed during verification: got %q, want %q", resolution.Digest, expectedDigest)
	}
	return nil
}

func (c GHCRChecker) validate(actor, token, repository, tag string) (*url.URL, error) {
	if c.Client == nil {
		return nil, errors.New("registry HTTP client is required")
	}
	if actor == "" || token == "" {
		return nil, errors.New("registry credentials are required")
	}
	if repository == "" || path.Clean(repository) != repository || strings.HasPrefix(repository, "/") {
		return nil, errors.New("invalid container repository")
	}
	if !versionTag.MatchString(tag) {
		return nil, errors.New("invalid container version tag")
	}
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.Path != "" {
		return nil, errors.New("invalid registry origin")
	}
	return origin, nil
}

func (c GHCRChecker) fetchToken(ctx context.Context, origin *url.URL, actor, token, repository string) (string, error) {
	tokenURL := *origin
	tokenURL.Path = "/token"
	query := tokenURL.Query()
	query.Set("service", "ghcr.io")
	query.Set("scope", "repository:"+repository+":pull")
	tokenURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create registry token request: %w", err)
	}
	req.SetBasicAuth(actor, token)
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request registry token: %w", sanitizeTransportError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request registry token: unexpected HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := decodeRegistryJSON(resp.Body, &payload); err != nil {
		return "", fmt.Errorf("decode registry token: %w", err)
	}
	if payload.Token == "" {
		payload.Token = payload.AccessToken
	}
	if payload.Token == "" {
		return "", errors.New("decode registry token: response has no token")
	}
	return payload.Token, nil
}

func (c GHCRChecker) verifyImageMetadata(ctx context.Context, origin *url.URL, token, repository, digest, version, revision string) error {
	body, returnedDigest, status, err := c.fetchManifest(ctx, origin, token, repository, digest)
	if err != nil {
		return err
	}
	if status != http.StatusOK || returnedDigest != digest {
		return fmt.Errorf("fetch image index by digest: HTTP %d digest %q", status, returnedDigest)
	}
	if err := verifyDigest(body, digest); err != nil {
		return fmt.Errorf("verify image index digest: %w", err)
	}
	var index struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Manifests     []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		return fmt.Errorf("decode image index: %w", err)
	}
	if index.SchemaVersion != 2 || (index.MediaType != "application/vnd.oci.image.index.v1+json" && index.MediaType != "application/vnd.docker.distribution.manifest.list.v2+json") {
		return errors.New("tag does not resolve to a supported multi-platform image index")
	}
	wanted := map[string]bool{"linux/amd64": false, "linux/arm64": false}
	for _, descriptor := range index.Manifests {
		platform := descriptor.Platform.OS + "/" + descriptor.Platform.Architecture
		if _, ok := wanted[platform]; !ok {
			if platform == "unknown/unknown" {
				continue
			}
			return fmt.Errorf("unexpected image platform %q", platform)
		}
		if wanted[platform] {
			return fmt.Errorf("duplicate image platform %q", platform)
		}
		if err := c.verifyPlatformConfig(ctx, origin, token, repository, descriptor.Digest, descriptor.Platform.OS, descriptor.Platform.Architecture, version, revision); err != nil {
			return fmt.Errorf("verify %s image metadata: %w", platform, err)
		}
		wanted[platform] = true
	}
	for platform, found := range wanted {
		if !found {
			return fmt.Errorf("image index is missing %s", platform)
		}
	}
	return nil
}

func (c GHCRChecker) verifyPlatformConfig(ctx context.Context, origin *url.URL, token, repository, manifestDigest, osName, architecture, version, revision string) error {
	if !digestRE.MatchString(manifestDigest) {
		return errors.New("invalid platform manifest digest")
	}
	body, returnedDigest, status, err := c.fetchManifest(ctx, origin, token, repository, manifestDigest)
	if err != nil {
		return err
	}
	if status != http.StatusOK || returnedDigest != manifestDigest {
		return fmt.Errorf("fetch platform manifest: HTTP %d digest %q", status, returnedDigest)
	}
	if err := verifyDigest(body, manifestDigest); err != nil {
		return fmt.Errorf("verify platform manifest digest: %w", err)
	}
	var manifest struct {
		SchemaVersion int `json:"schemaVersion"`
		Config        struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("decode platform manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 || !digestRE.MatchString(manifest.Config.Digest) {
		return errors.New("invalid platform manifest config")
	}
	configBody, err := c.fetchBlob(ctx, origin, token, repository, manifest.Config.Digest)
	if err != nil {
		return err
	}
	var config struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Config       struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(configBody, &config); err != nil {
		return fmt.Errorf("decode platform config: %w", err)
	}
	if config.OS != osName || config.Architecture != architecture {
		return fmt.Errorf("platform config is %s/%s", config.OS, config.Architecture)
	}
	if config.Config.Labels["org.opencontainers.image.version"] != version {
		return errors.New("platform config has wrong version label")
	}
	if config.Config.Labels["org.opencontainers.image.revision"] != revision {
		return errors.New("platform config has wrong revision label")
	}
	return nil
}

func (c GHCRChecker) fetchManifest(ctx context.Context, origin *url.URL, token, repository, reference string) ([]byte, string, int, error) {
	manifestURL := *origin
	manifestURL.Path = "/v2/" + repository + "/manifests/" + url.PathEscape(reference)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL.String(), nil)
	if err != nil {
		return nil, "", 0, fmt.Errorf("create registry manifest request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, "", 0, fmt.Errorf("request registry manifest: %w", sanitizeTransportError(err))
	}
	defer resp.Body.Close()
	body, err := readRegistryBody(resp.Body)
	if err != nil {
		return nil, "", 0, fmt.Errorf("read registry manifest: %w", err)
	}
	return body, resp.Header.Get("Docker-Content-Digest"), resp.StatusCode, nil
}

func (c GHCRChecker) fetchBlob(ctx context.Context, origin *url.URL, token, repository, digest string) ([]byte, error) {
	blobURL := *origin
	blobURL.Path = "/v2/" + repository + "/blobs/" + digest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blobURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create registry blob request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request registry blob: %w", sanitizeTransportError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request registry blob: unexpected HTTP %d", resp.StatusCode)
	}
	body, err := readRegistryBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read registry blob: %w", err)
	}
	if err := verifyDigest(body, digest); err != nil {
		return nil, fmt.Errorf("verify registry blob: %w", err)
	}
	return body, nil
}

func verifyDigest(data []byte, expected string) error {
	if !digestRE.MatchString(expected) {
		return fmt.Errorf("invalid digest %q", expected)
	}
	sum := sha256.Sum256(data)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("digest mismatch: got %s, want %s", actual, expected)
	}
	return nil
}

func decodeRegistryJSON(reader io.Reader, target any) error {
	data, err := readRegistryBody(reader)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func readRegistryBody(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxRegistryResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRegistryResponseBytes {
		return nil, errors.New("response exceeds size limit")
	}
	return data, nil
}

func sanitizeTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	location := "<redacted>"
	var urlError *url.Error
	if errors.As(err, &urlError) {
		if parsed, parseErr := url.Parse(urlError.URL); parseErr == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.ForceQuery = false
			parsed.Fragment = ""
			location = parsed.String()
		}
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return fmt.Errorf("HTTP transport timeout for %s", location)
	}
	return fmt.Errorf("HTTP transport failed for %s", location)
}
