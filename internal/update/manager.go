package update

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

const (
	maxManifestBytes       = 256 << 10
	maxBundleBytes         = 512 << 10
	maxPersistedStateBytes = 16 << 10
)

type PolicyStore interface {
	AutoUpdate(context.Context) (bool, error)
	SetAutoUpdate(context.Context, bool) error
}

type Installer interface {
	Supported() (bool, string)
	Apply(context.Context, Manifest, Asset) error
}

type Options struct {
	CurrentVersion string
	Revision       string
	BuiltAt        string
	DataDirectory  string
	ChecksEnabled  bool
	Policy         PolicyStore
	Verifier       Verifier
	Installer      Installer
	HTTPClient     *http.Client
	ManifestURL    string
	BundleURL      string
}

type Manager struct {
	mu             sync.Mutex
	currentVersion string
	revision       string
	builtAt        string
	checksEnabled  bool
	policy         PolicyStore
	verifier       Verifier
	installer      Installer
	httpClient     *http.Client
	manifestURL    string
	bundleURL      string
	statePath      string
	status         Status
	verified       *Manifest
	applying       bool
	checking       bool
}

func NewManager(options Options) *Manager {
	if options.CurrentVersion == "" {
		options.CurrentVersion = "dev"
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if options.ManifestURL == "" {
		options.ManifestURL = manifestURL
	}
	if options.BundleURL == "" {
		options.BundleURL = bundleURL
	}
	applyMode := "notification_only"
	message := "This installation can notify about releases but cannot replace itself."
	if options.Installer != nil {
		if supported, reason := options.Installer.Supported(); supported {
			applyMode = "automatic"
			message = ""
		} else if reason != "" {
			message = reason
		}
	}
	manager := &Manager{
		currentVersion: options.CurrentVersion,
		revision:       options.Revision,
		builtAt:        options.BuiltAt,
		checksEnabled:  options.ChecksEnabled,
		policy:         options.Policy,
		verifier:       options.Verifier,
		installer:      options.Installer,
		httpClient:     options.HTTPClient,
		manifestURL:    options.ManifestURL,
		bundleURL:      options.BundleURL,
		statePath:      filepath.Join(options.DataDirectory, "update", "highest-seen.json"),
		status:         Status{State: "idle", CurrentVersion: options.CurrentVersion, ApplyMode: applyMode},
	}
	if message != "" {
		manager.status.Message = &message
	}
	if err := manager.loadPersistedStatus(); err != nil {
		message := err.Error()
		manager.status.State = "failed"
		manager.status.Message = &message
	}
	return manager
}

func (m *Manager) Version() VersionInfo {
	return VersionInfo{Version: m.currentVersion, Revision: m.revision, BuiltAt: m.builtAt}
}

func (m *Manager) Status(ctx context.Context, canManage bool) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status
	status.CanManage = canManage
	if m.policy != nil {
		if enabled, err := m.policy.AutoUpdate(ctx); err == nil {
			status.AutoApply = enabled
		}
	}
	return status
}

func (m *Manager) SetAutoApply(ctx context.Context, enabled bool) error {
	m.mu.Lock()
	mode := m.status.ApplyMode
	m.mu.Unlock()
	if mode != "automatic" && enabled {
		return fmt.Errorf("automatic updates are unavailable for this installation")
	}
	if m.policy == nil {
		return fmt.Errorf("update policy store is unavailable")
	}
	return m.policy.SetAutoUpdate(ctx, enabled)
}

func (m *Manager) Check(ctx context.Context) error {
	if !m.checksEnabled {
		return fmt.Errorf("update checks are disabled by WORKTIME_UPDATE_CHECKS=0")
	}
	m.mu.Lock()
	if m.checking || m.applying {
		m.mu.Unlock()
		return fmt.Errorf("an update operation is already in progress")
	}
	m.checking = true
	// A failed refresh must not leave an older in-memory authorization usable.
	// Persisted metadata is display-only, and every apply requires the current
	// process to have completed this verification successfully.
	m.verified = nil
	m.status.ApplyReady = false
	m.status.State = "checking"
	m.status.Message = nil
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.checking = false
		m.mu.Unlock()
	}()

	manifestBytes, err := m.fetchBounded(ctx, m.manifestURL, maxManifestBytes)
	if err != nil {
		return m.fail(fmt.Errorf("fetch release manifest: %w", err))
	}
	bundleBytes, err := m.fetchBounded(ctx, m.bundleURL, maxBundleBytes)
	if err != nil {
		return m.fail(fmt.Errorf("fetch release signature: %w", err))
	}
	if m.verifier == nil {
		return m.fail(fmt.Errorf("release verifier is unavailable"))
	}
	if err := m.verifier.Verify(ctx, manifestBytes, bundleBytes); err != nil {
		return m.fail(err)
	}
	manifest, err := decodeManifest(manifestBytes)
	if err != nil {
		return m.fail(err)
	}
	checkedAt := time.Now().UnixMilli()
	if err := m.acceptVerifiedManifest(manifest, checkedAt); err != nil {
		return m.fail(err)
	}
	latest := manifest.Version
	changelog := manifest.ChangelogURL
	available := newerVersion(manifest.Version, m.currentVersion)
	m.mu.Lock()
	m.verified = &manifest
	m.status.State = "up_to_date"
	if available {
		m.status.State = "available"
	}
	m.status.LatestVersion = &latest
	m.status.UpdateAvailable = available
	m.status.ApplyReady = available && m.status.ApplyMode == "automatic"
	m.status.CheckedAt = &checkedAt
	m.status.ChangelogURL = &changelog
	m.status.Message = nil
	m.mu.Unlock()
	return nil
}

func (m *Manager) Apply(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.applying || m.checking {
		return fmt.Errorf("an update is already being applied")
	}
	if m.status.ApplyMode != "automatic" || m.installer == nil {
		return fmt.Errorf("self-update is unavailable for this installation")
	}
	if m.status.State != "available" || m.verified == nil || !m.status.UpdateAvailable || !m.status.ApplyReady {
		return fmt.Errorf("no verified update is available")
	}
	manifest := *m.verified
	asset, ok := selectAsset(manifest.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("release has no asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	m.applying = true
	m.status.State = "applying"
	// Applying consumes this authorization. A failed handoff must be followed by
	// another signed check before an administrator can retry.
	m.status.ApplyReady = false
	go func() {
		err := m.installer.Apply(context.Background(), manifest, asset)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.applying = false
		if err != nil {
			message := err.Error()
			m.status.State = "failed"
			m.status.Message = &message
		}
	}()
	return nil
}

func (m *Manager) fail(err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	message := err.Error()
	m.status.State = "failed"
	m.status.Message = &message
	return err
}

func (m *Manager) fetchBounded(ctx context.Context, target string, maximum int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	return data, nil
}

func decodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, fmt.Errorf("decode release manifest: trailing data")
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != 1 || manifest.Generation == 0 || !semver.IsValid(manifest.Version) ||
		len(manifest.Revision) != 40 || manifest.PublishedAt.IsZero() {
		return fmt.Errorf("release manifest has invalid required fields")
	}
	if _, err := hex.DecodeString(manifest.Revision); err != nil {
		return fmt.Errorf("release manifest has invalid revision")
	}
	if !validChangelogURL(manifest.Version, manifest.ChangelogURL) {
		return fmt.Errorf("release manifest has invalid changelog URL")
	}
	if manifest.Image.Name != "ghcr.io/siyet/worktime" ||
		!strings.HasPrefix(manifest.Image.Digest, "sha256:") ||
		len(manifest.Image.Digest) != len("sha256:")+64 {
		return fmt.Errorf("release manifest has invalid image digest")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(manifest.Image.Digest, "sha256:")); err != nil {
		return fmt.Errorf("release manifest has invalid image digest")
	}
	seen := make(map[string]bool)
	for _, asset := range manifest.Assets {
		expectedName := "worktime-linux-" + asset.Arch
		if asset.Size <= 0 || len(asset.SHA256) != 64 || asset.Name == "" ||
			asset.OS != "linux" || (asset.Arch != "amd64" && asset.Arch != "arm64") ||
			asset.Name != expectedName || seen[asset.OS+"/"+asset.Arch] {
			return fmt.Errorf("release manifest has invalid or duplicate assets")
		}
		if _, err := hex.DecodeString(asset.SHA256); err != nil {
			return fmt.Errorf("release manifest has invalid asset digest")
		}
		seen[asset.OS+"/"+asset.Arch] = true
		parsedAsset, err := url.Parse(asset.URL)
		if err != nil || parsedAsset.Scheme != "https" || parsedAsset.Host != "github.com" ||
			parsedAsset.RawQuery != "" || parsedAsset.Fragment != "" ||
			parsedAsset.Path != "/Siyet/worktime/releases/download/"+manifest.Version+"/"+expectedName {
			return fmt.Errorf("release manifest has untrusted asset URL")
		}
	}
	if !seen["linux/amd64"] || !seen["linux/arm64"] {
		return fmt.Errorf("release manifest does not contain both Linux assets")
	}
	return nil
}

func validChangelogURL(version, target string) bool {
	parsed, err := url.Parse(target)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "github.com" &&
		parsed.RawQuery == "" && parsed.Fragment == "" &&
		parsed.Path == "/Siyet/worktime/releases/tag/"+version
}

func (m *Manager) loadPersistedStatus() error {
	state, found, err := readPersistedState(m.statePath)
	if err != nil {
		return fmt.Errorf("read highest-seen release state: %w", err)
	}
	if !found {
		return nil
	}
	latest := state.Version
	checkedAt := state.CheckedAt
	changelog := state.ChangelogURL
	m.status.State = "up_to_date"
	if newerVersion(latest, m.currentVersion) {
		m.status.State = "available"
		m.status.UpdateAvailable = true
	}
	m.status.LatestVersion = &latest
	m.status.CheckedAt = &checkedAt
	m.status.ChangelogURL = &changelog
	return nil
}

func (m *Manager) acceptVerifiedManifest(manifest Manifest, checkedAt int64) error {
	state, found, err := readPersistedState(m.statePath)
	if err != nil {
		return fmt.Errorf("read highest-seen release state: %w", err)
	}
	if manifest.Generation < state.Generation ||
		(manifest.Generation == state.Generation && state.Version != "" && manifest.Version != state.Version) {
		return fmt.Errorf("release manifest is a replay or generation mismatch")
	}
	if state.Version != "" && semver.Compare(manifest.Version, state.Version) < 0 {
		return fmt.Errorf("release manifest attempts a downgrade")
	}
	if found && manifest.Generation == state.Generation && manifest.Version == state.Version &&
		state.ChangelogURL != manifest.ChangelogURL {
		return fmt.Errorf("release manifest changes data without advancing generation")
	}
	return writeJSONAtomic(m.statePath, persistedState{
		SchemaVersion: 1,
		Generation:    manifest.Generation,
		Version:       manifest.Version,
		CheckedAt:     checkedAt,
		ChangelogURL:  manifest.ChangelogURL,
	})
}

func readPersistedState(path string) (persistedState, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedState{}, false, nil
	}
	if err != nil {
		return persistedState{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPersistedStateBytes+1))
	if err != nil {
		return persistedState{}, false, err
	}
	if len(data) > maxPersistedStateBytes {
		return persistedState{}, false, fmt.Errorf("persisted update state exceeds %d bytes", maxPersistedStateBytes)
	}
	state := persistedState{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return persistedState{}, false, fmt.Errorf("invalid persisted update state")
	}
	if state.SchemaVersion != 1 || state.Generation == 0 || !semver.IsValid(state.Version) ||
		state.CheckedAt <= 0 || !validChangelogURL(state.Version, state.ChangelogURL) {
		return persistedState{}, false, fmt.Errorf("invalid persisted update state")
	}
	return state, true, nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := path + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}

func newerVersion(latest, current string) bool {
	if !semver.IsValid(current) {
		return true
	}
	return semver.Compare(latest, current) > 0
}

func selectAsset(assets []Asset, operatingSystem, architecture string) (Asset, bool) {
	for _, asset := range assets {
		if asset.OS == operatingSystem && asset.Arch == architecture {
			return asset, true
		}
	}
	return Asset{}, false
}
