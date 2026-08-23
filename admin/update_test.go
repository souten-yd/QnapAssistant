package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetUpdateTestGlobals() {
	updateReleaseAPI = defaultUpdateReleaseAPI
	updateHTTPClient = &http.Client{Timeout: 15 * time.Second}
	updateCache.Lock()
	updateCache.apiURL = ""
	updateCache.at = time.Time{}
	updateCache.info = updateReleaseInfo{}
	updateCache.Unlock()
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.5.1", "0.5.2", -1, true},
		{"v0.5.2", "0.5.2", 0, true},
		{"0.6.0", "0.5.9", 1, true},
		{"1.0.0", "0.99.99", 1, true},
		{"dev", "0.5.2", 0, false},
	}
	for _, tc := range tests {
		got, ok := compareSemver(tc.a, tc.b)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("compareSemver(%q,%q)=(%d,%v), want (%d,%v)", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

func TestReleaseInfoRequiresMatchingChecksum(t *testing.T) {
	rel := githubRelease{
		TagName: "v0.5.2",
		Assets: []githubReleaseAsset{
			{Name: "QnapAssistant_0.5.2_x86_64.qpkg", BrowserDownloadURL: "https://github.com/souten-yd/QnapAssistant/releases/download/v0.5.2/QnapAssistant_0.5.2_x86_64.qpkg"},
		},
	}
	if _, err := releaseInfoFromGitHub(rel); err == nil || !strings.Contains(err.Error(), ".sha256") {
		t.Fatalf("expected missing checksum error, got %v", err)
	}
	rel.Assets = append(rel.Assets, githubReleaseAsset{Name: "QnapAssistant_0.5.2_x86_64.qpkg.sha256", BrowserDownloadURL: "https://github.com/souten-yd/QnapAssistant/releases/download/v0.5.2/QnapAssistant_0.5.2_x86_64.qpkg.sha256"})
	info, err := releaseInfoFromGitHub(rel)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "0.5.2" || info.PackageName != "QnapAssistant_0.5.2_x86_64.qpkg" {
		t.Fatalf("unexpected release info: %+v", info)
	}
}

func TestUpdateCheckUsesStableGitHubRelease(t *testing.T) {
	defer resetUpdateTestGlobals()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "QnapAssistant-self-update" {
			t.Errorf("unexpected User-Agent %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name":"v0.5.2",
			"name":"QnapAssistant v0.5.2",
			"html_url":"https://github.com/souten-yd/QnapAssistant/releases/tag/v0.5.2",
			"body":"Self update release",
			"draft":false,
			"prerelease":false,
			"published_at":"2026-08-23T00:00:00Z",
			"assets":[
				{"name":"QnapAssistant_0.5.2_x86_64.qpkg","browser_download_url":"https://github.com/souten-yd/QnapAssistant/releases/download/v0.5.2/QnapAssistant_0.5.2_x86_64.qpkg","size":1234},
				{"name":"QnapAssistant_0.5.2_x86_64.qpkg.sha256","browser_download_url":"https://github.com/souten-yd/QnapAssistant/releases/download/v0.5.2/QnapAssistant_0.5.2_x86_64.qpkg.sha256","size":128}
			]
		}`))
	}))
	defer server.Close()
	updateReleaseAPI = server.URL
	updateHTTPClient = server.Client()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "qpkg.cfg"), []byte("QPKG_VER=\"0.5.1\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	m := &manager{qpkgDir: dir}
	req := httptest.NewRequest(http.MethodGet, "/api/update/check", nil)
	rr := httptest.NewRecorder()
	m.handleUpdateCheck(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["current_version"] != "0.5.1" || got["latest_version"] != "0.5.2" || got["available"] != true {
		t.Fatalf("unexpected response: %#v", got)
	}
	if got["package_name"] != "QnapAssistant_0.5.2_x86_64.qpkg" {
		t.Fatalf("unexpected package: %#v", got["package_name"])
	}
}

func TestSelfUpdateRequiresSameOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://nas:11435/api/update/apply", nil)
	req.Host = "nas:11435"
	req.Header.Set("Origin", "http://evil.example")
	if sameOriginUpdateRequest(req) {
		t.Fatal("cross-origin update request should be rejected")
	}
	req.Header.Set("Origin", "http://nas:11435")
	if !sameOriginUpdateRequest(req) {
		t.Fatal("same-origin update request should be accepted")
	}
}

func TestUpdateUIInjectedOnce(t *testing.T) {
	html := injectUpdateUI(renderedIndexHTML())
	if strings.Count(html, `id="updateCard"`) != 1 {
		t.Fatalf("expected exactly one update card")
	}
	for _, want := range []string{"/api/update/check", "/api/update/apply", "X-Qnap-Update-Confirm", "今すぐ更新", "SHA-256"} {
		if !strings.Contains(html, want) {
			t.Fatalf("update UI missing %q", want)
		}
	}
}
