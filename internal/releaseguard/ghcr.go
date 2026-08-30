package releaseguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const maxRegistryResponseBytes = 1 << 20

var (
	ErrTagExists = errors.New("container image tag already exists")
	versionTag   = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

type GHCRChecker struct {
	Client *http.Client
	Origin string
}

func (c GHCRChecker) RequireTagAbsent(ctx context.Context, actor, token, repository, tag string) error {
	if c.Client == nil {
		return errors.New("registry HTTP client is required")
	}
	if actor == "" || token == "" {
		return errors.New("registry credentials are required")
	}
	if repository == "" || path.Clean(repository) != repository || strings.HasPrefix(repository, "/") {
		return errors.New("invalid container repository")
	}
	if !versionTag.MatchString(tag) {
		return errors.New("invalid container version tag")
	}
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.Path != "" {
		return errors.New("invalid registry origin")
	}

	registryToken, err := c.fetchToken(ctx, origin, actor, token, repository)
	if err != nil {
		return err
	}
	return c.checkManifest(ctx, origin, registryToken, repository, tag)
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
		return "", fmt.Errorf("request registry token: %w", err)
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

func (c GHCRChecker) checkManifest(ctx context.Context, origin *url.URL, token, repository, tag string) error {
	manifestURL := *origin
	manifestURL.Path = "/v2/" + repository + "/manifests/" + url.PathEscape(tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create registry manifest request: %w", err)
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
		return fmt.Errorf("request registry manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return ErrTagExists
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("request registry manifest: unexpected HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := decodeRegistryJSON(resp.Body, &payload); err != nil {
		return fmt.Errorf("decode missing registry manifest: %w", err)
	}
	if len(payload.Errors) != 1 || payload.Errors[0].Code != "MANIFEST_UNKNOWN" {
		return errors.New("registry returned HTTP 404 without exact MANIFEST_UNKNOWN status")
	}
	return nil
}

func decodeRegistryJSON(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxRegistryResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxRegistryResponseBytes {
		return errors.New("response exceeds size limit")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}
