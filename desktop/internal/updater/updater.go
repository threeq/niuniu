package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/go-shared/releasecheck"
	"golang.org/x/mod/semver"
)

var Version = "dev"

type UpdateResult struct {
	Available   bool   `json:"available"`
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	ReleaseURL  string `json:"release_url"`
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	HTMLURL string        `json:"html_url"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type Updater struct {
	currentVersion string
	apiURL         string
	websiteBaseURL string // non-empty → check the official website changelog instead of GitHub
	skippedVersion string
	client         *http.Client
}

func New(currentVersion, apiURL string) *Updater {
	return &Updater{currentVersion: currentVersion, apiURL: apiURL, client: &http.Client{Timeout: 15 * time.Second}}
}

func NewGitHub(currentVersion, owner, repo string) *Updater {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	return New(currentVersion, url)
}

// NewWebsite checks the official website changelog (<baseURL>/changelog) rather
// than api.github.com, which returns HTTP 403 from mainland China. Pass
// releasecheck.DefaultBaseURL for production.
func NewWebsite(currentVersion, baseURL string) *Updater {
	return &Updater{currentVersion: currentVersion, websiteBaseURL: baseURL, client: &http.Client{Timeout: 15 * time.Second}}
}

func (u *Updater) SetSkipped(version string) { u.skippedVersion = version }

// fetchLatest returns the latest release in the GitHub-shaped struct, from
// whichever source this Updater was configured with.
func (u *Updater) fetchLatest() (githubRelease, error) {
	if u.websiteBaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rel, err := releasecheck.FetchLatest(ctx, u.client, u.websiteBaseURL)
		if err != nil {
			return githubRelease{}, err
		}
		gr := githubRelease{TagName: rel.TagName, HTMLURL: rel.HTMLURL}
		for _, a := range rel.Assets {
			gr.Assets = append(gr.Assets, githubAsset{Name: a.Name, BrowserDownloadURL: a.BrowserDownloadURL})
		}
		return gr, nil
	}
	resp, err := u.client.Get(u.apiURL)
	if err != nil {
		return githubRelease{}, fmt.Errorf("check update: %w", err)
	}
	defer resp.Body.Close()
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("decode release: %w", err)
	}
	return release, nil
}

func (u *Updater) Check() (*UpdateResult, error) {
	release, err := u.fetchLatest()
	if err != nil {
		return nil, err
	}
	result := &UpdateResult{Version: release.TagName, ReleaseURL: release.HTMLURL}
	if release.TagName == u.currentVersion || release.TagName == u.skippedVersion {
		return result, nil
	}
	// semver.Compare returns +1 if release is newer than current
	if semver.Compare(release.TagName, u.currentVersion) > 0 {
		result.Available = true
		result.DownloadURL = u.findPlatformAsset(release.Assets)
	}
	return result, nil
}

func (u *Updater) findPlatformAsset(assets []githubAsset) string {
	os := runtime.GOOS
	arch := runtime.GOARCH
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, os) && strings.Contains(name, arch) {
			return a.BrowserDownloadURL
		}
	}
	for _, a := range assets {
		if strings.Contains(strings.ToLower(a.Name), os) {
			return a.BrowserDownloadURL
		}
	}
	slog.Warn("no matching asset found", "os", os, "arch", arch)
	return ""
}
