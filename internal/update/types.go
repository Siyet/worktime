// Package update discovers and applies cryptographically verified WorkTime releases.
package update

import "time"

const (
	manifestURL = "https://github.com/Siyet/worktime/releases/latest/download/release-manifest.json"
	bundleURL   = "https://github.com/Siyet/worktime/releases/latest/download/release-manifest.sigstore.json"
)

type Asset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Image struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Generation    uint64    `json:"generation"`
	Version       string    `json:"version"`
	Revision      string    `json:"revision"`
	PublishedAt   time.Time `json:"published_at"`
	ChangelogURL  string    `json:"changelog_url"`
	Image         Image     `json:"image"`
	Assets        []Asset   `json:"assets"`
}

type VersionInfo struct {
	Version  string `json:"version"`
	Revision string `json:"revision,omitempty"`
	BuiltAt  string `json:"built_at,omitempty"`
}

type Status struct {
	State           string  `json:"state"`
	CurrentVersion  string  `json:"current_version"`
	LatestVersion   *string `json:"latest_version"`
	UpdateAvailable bool    `json:"update_available"`
	ApplyReady      bool    `json:"apply_ready"`
	CheckedAt       *int64  `json:"checked_at"`
	ChangelogURL    *string `json:"changelog_url"`
	AutoApply       bool    `json:"auto_apply"`
	CanManage       bool    `json:"can_manage"`
	ApplyMode       string  `json:"apply_mode"`
	Message         *string `json:"message"`
}

type persistedState struct {
	SchemaVersion int    `json:"schema_version"`
	Generation    uint64 `json:"generation"`
	Version       string `json:"version"`
	CheckedAt     int64  `json:"checked_at"`
	ChangelogURL  string `json:"changelog_url"`
}
