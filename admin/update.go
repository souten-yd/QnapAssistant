package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultUpdateReleaseAPI = "https://api.github.com/repos/souten-yd/QnapAssistant/releases/latest"
	updateStatePath         = "/share/Public/QnapAssistant/update-state"
	updateLogPath           = "/share/Public/QnapAssistant/update.log"
)

var (
	updateReleaseAPI = defaultUpdateReleaseAPI
	updateHTTPClient = &http.Client{Timeout: 15 * time.Second}
	semverPattern    = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)$`)
	qpkgVersionLine  = regexp.MustCompile(`(?m)^QPKG_VER="([^"]+)"$`)
	updateCache      struct {
		sync.Mutex
		apiURL string
		at     time.Time
		info   updateReleaseInfo
	}
)

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	HTMLURL     string               `json:"html_url"`
	Body        string               `json:"body"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type updateReleaseInfo struct {
	Version         string
	Name            string
	ReleaseURL      string
	Notes           string
	PublishedAt     string
	PackageName     string
	PackageURL      string
	PackageSize     int64
	ChecksumName    string
	ChecksumURL     string
}

type updateState struct {
	Status        string `json:"status"`
	TargetVersion string `json:"target_version,omitempty"`
	Message       string `json:"message,omitempty"`
	UpdatedAt     int64  `json:"updated_at,omitempty"`
}

func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	m := semverPattern.FindStringSubmatch(strings.TrimSpace(s))
	if len(m) != 4 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func compareSemver(a, b string) (int, bool) {
	av, ok := parseSemver(a)
	if !ok {
		return 0, false
	}
	bv, ok := parseSemver(b)
	if !ok {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if av[i] < bv[i] {
			return -1, true
		}
		if av[i] > bv[i] {
			return 1, true
		}
	}
	return 0, true
}

func normalizeVersion(s string) (string, bool) {
	v, ok := parseSemver(s)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]), true
}

func versionFromQPKGConfig(data []byte) string {
	m := qpkgVersionLine.FindSubmatch(data)
	if len(m) != 2 {
		return ""
	}
	v, ok := normalizeVersion(string(m[1]))
	if !ok {
		return ""
	}
	return v
}

func (m *manager) currentQPKGVersion() string {
	if out, err := exec.Command("/sbin/getcfg", "QnapAssistant", "Version", "-f", "/etc/config/qpkg.conf").Output(); err == nil {
		if v, ok := normalizeVersion(strings.TrimSpace(string(out))); ok {
			return v
		}
	}
	for _, path := range []string{filepath.Join(m.qpkgDir, "qpkg.cfg"), filepath.Join(m.qpkgDir, "../qpkg.cfg")} {
		if data, err := os.ReadFile(path); err == nil {
			if v := versionFromQPKGConfig(data); v != "" {
				return v
			}
		}
	}
	return "unknown"
}

func validateReleaseAssetURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return false
	}
	return strings.HasPrefix(u.Path, "/souten-yd/QnapAssistant/releases/download/")
}

func releaseInfoFromGitHub(rel githubRelease) (updateReleaseInfo, error) {
	if rel.Draft || rel.Prerelease {
		return updateReleaseInfo{}, errors.New("latest release is not a stable published release")
	}
	version, ok := normalizeVersion(rel.TagName)
	if !ok {
		return updateReleaseInfo{}, fmt.Errorf("invalid release tag %q", rel.TagName)
	}
	var pkg, sum *githubReleaseAsset
	for i := range rel.Assets {
		a := &rel.Assets[i]
		if strings.HasSuffix(strings.ToLower(a.Name), ".qpkg") && filepath.Base(a.Name) == a.Name {
			if pkg != nil {
				return updateReleaseInfo{}, errors.New("release contains multiple QPKG assets")
			}
			pkg = a
		}
	}
	if pkg == nil {
		return updateReleaseInfo{}, errors.New("release does not contain a QPKG asset")
	}
	for i := range rel.Assets {
		a := &rel.Assets[i]
		if a.Name == pkg.Name+".sha256" {
			sum = a
			break
		}
	}
	if sum == nil {
		return updateReleaseInfo{}, errors.New("release does not contain the matching QPKG .sha256 asset")
	}
	if !validateReleaseAssetURL(pkg.BrowserDownloadURL) || !validateReleaseAssetURL(sum.BrowserDownloadURL) {
		return updateReleaseInfo{}, errors.New("release assets are not hosted under the expected GitHub repository")
	}
	return updateReleaseInfo{
		Version:      version,
		Name:         rel.Name,
		ReleaseURL:   rel.HTMLURL,
		Notes:        rel.Body,
		PublishedAt:  rel.PublishedAt,
		PackageName:  pkg.Name,
		PackageURL:   pkg.BrowserDownloadURL,
		PackageSize:  pkg.Size,
		ChecksumName: sum.Name,
		ChecksumURL:  sum.BrowserDownloadURL,
	}, nil
}

func fetchLatestUpdateRelease(ctx context.Context, force bool) (updateReleaseInfo, error) {
	updateCache.Lock()
	if !force && updateCache.apiURL == updateReleaseAPI && !updateCache.at.IsZero() && time.Since(updateCache.at) < 5*time.Minute {
		info := updateCache.info
		updateCache.Unlock()
		return info, nil
	}
	updateCache.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateReleaseAPI, nil)
	if err != nil {
		return updateReleaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "QnapAssistant-self-update")
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return updateReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return updateReleaseInfo{}, fmt.Errorf("GitHub Releases returned HTTP %d", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return updateReleaseInfo{}, err
	}
	info, err := releaseInfoFromGitHub(rel)
	if err != nil {
		return updateReleaseInfo{}, err
	}
	updateCache.Lock()
	updateCache.apiURL = updateReleaseAPI
	updateCache.at = time.Now()
	updateCache.info = info
	updateCache.Unlock()
	return info, nil
}

func readUpdateState(path string) updateState {
	state := updateState{Status: "idle"}
	f, err := os.Open(path)
	if err != nil {
		return state
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "status":
			state.Status = value
		case "target_version":
			state.TargetVersion = value
		case "message":
			state.Message = value
		case "updated_at":
			state.UpdatedAt, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	return state
}

func writeUpdateState(path string, state updateState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	clean := func(v string) string {
		v = strings.ReplaceAll(v, "\n", " ")
		v = strings.ReplaceAll(v, "\r", " ")
		return strings.ReplaceAll(v, "=", "-")
	}
	body := fmt.Sprintf("status=%s\ntarget_version=%s\nmessage=%s\nupdated_at=%d\n", clean(state.Status), clean(state.TargetVersion), clean(state.Message), state.UpdatedAt)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sameOriginUpdateRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host) && (u.Scheme == "http" || u.Scheme == "https")
}

func (m *manager) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	info, err := fetchLatestUpdateRelease(r.Context(), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	current := m.currentQPKGVersion()
	available := false
	if cmp, ok := compareSemver(current, info.Version); ok {
		available = cmp < 0
	}
	writeJSON(w, map[string]any{
		"current_version": current,
		"latest_version": info.Version,
		"available":       available,
		"release_name":    info.Name,
		"release_url":     info.ReleaseURL,
		"notes":           info.Notes,
		"published_at":    info.PublishedAt,
		"package_name":    info.PackageName,
		"package_size":    info.PackageSize,
	})
}

func (m *manager) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	state := readUpdateState(updateStatePath)
	writeJSON(w, map[string]any{
		"status":          state.Status,
		"target_version":  state.TargetVersion,
		"message":         state.Message,
		"updated_at":      state.UpdatedAt,
		"current_version": m.currentQPKGVersion(),
	})
}

func (m *manager) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginUpdateRequest(r) {
		http.Error(w, "self-update must be initiated from this QnapAssistant web UI", http.StatusForbidden)
		return
	}
	if r.Header.Get("X-Qnap-Update-Confirm") != "install" {
		http.Error(w, "missing update confirmation", http.StatusBadRequest)
		return
	}
	state := readUpdateState(updateStatePath)
	switch state.Status {
	case "downloading", "verifying", "installing", "restarting":
		http.Error(w, "an update is already in progress", http.StatusConflict)
		return
	}
	if _, err := os.Stat("/sbin/qpkg_cli"); err != nil {
		http.Error(w, "QNAP qpkg_cli is unavailable on this system", http.StatusServiceUnavailable)
		return
	}
	info, err := fetchLatestUpdateRelease(r.Context(), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	current := m.currentQPKGVersion()
	cmp, ok := compareSemver(current, info.Version)
	if !ok {
		http.Error(w, "cannot determine the installed QPKG version", http.StatusConflict)
		return
	}
	if cmp >= 0 {
		http.Error(w, "QnapAssistant is already up to date", http.StatusConflict)
		return
	}

	helperSource := filepath.Join(m.qpkgDir, "update-self.sh")
	helper, err := os.ReadFile(helperSource)
	if err != nil {
		http.Error(w, "self-update helper is unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpHelper := filepath.Join("/tmp", fmt.Sprintf("qnapassistant-update-%d.sh", os.Getpid()))
	if err := os.WriteFile(tmpHelper, helper, 0700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeUpdateState(updateStatePath, updateState{Status: "starting", TargetVersion: info.Version, Message: "Updater started", UpdatedAt: time.Now().Unix()}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logFile, err := os.OpenFile(updateLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cmd := exec.Command("/bin/sh", tmpHelper, info.PackageURL, info.ChecksumURL, info.Version, info.PackageName, updateStatePath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		_ = writeUpdateState(updateStatePath, updateState{Status: "failed", TargetVersion: info.Version, Message: err.Error(), UpdatedAt: time.Now().Unix()})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		_ = os.Remove(tmpHelper)
	}()
	writeJSON(w, map[string]any{"ok": true, "target_version": info.Version, "status": "starting"})
}
