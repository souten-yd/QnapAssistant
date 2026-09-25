package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func downloadTestManager(t *testing.T) *manager {
	t.Helper()
	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	if err := saveConfig(m.configPath, config{"MODEL_DIR": dir, "MODEL_PATH": filepath.Join(dir, "old.gguf")}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestModelDownloadValidRedirectAndSelection(t *testing.T) {
	data := "GGUF" + strings.Repeat("x", 128)
	hash := sha256.Sum256([]byte(data))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/file", 302)
			return
		}
		fmt.Fprint(w, data)
	}))
	defer server.Close()
	m := downloadTestManager(t)
	body := fmt.Sprintf(`{"URL":%q,"Filename":"new.gguf","SHA256":%q}`, server.URL+"/redirect", hex.EncodeToString(hash[:]))
	rec := httptest.NewRecorder()
	m.handleModelDownload(rec, httptest.NewRequest("POST", "/api/models/download", strings.NewReader(body)))
	if rec.Code != 202 {
		t.Fatalf("start %d: %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		d := m.download
		m.mu.Unlock()
		if !d.Active {
			if d.Status != "complete" || d.Written != int64(len(data)) || d.Error != "" {
				t.Fatalf("state %#v", d)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	rec = httptest.NewRecorder()
	m.handleModelDownload(rec, httptest.NewRequest("GET", "/api/models/download", nil))
	var d downloadState
	json.Unmarshal(rec.Body.Bytes(), &d)
	if d.Status != "complete" || d.FinishedAt.IsZero() {
		t.Fatalf("status API %#v", d)
	}
	rec = httptest.NewRecorder()
	m.handleModels(rec, httptest.NewRequest("GET", "/api/models", nil))
	if !strings.Contains(rec.Body.String(), "new.gguf") || strings.Contains(rec.Body.String(), ".part") {
		t.Fatal(rec.Body.String())
	}
	cfg, _ := loadConfig(m.configPath)
	if filepath.Base(cfg["MODEL_PATH"]) != "old.gguf" {
		t.Fatal("download changed selected model")
	}
	rec = httptest.NewRecorder()
	m.handleModelSelect(rec, httptest.NewRequest("POST", "/api/models/select", strings.NewReader(fmt.Sprintf(`{"path":%q}`, d.Path))))
	if rec.Code != 200 {
		t.Fatalf("select %d: %s", rec.Code, rec.Body.String())
	}
	cfg, _ = loadConfig(m.configPath)
	if cfg["MODEL_PATH"] != d.Path {
		t.Fatal("selection not saved")
	}
}

func TestModelDownloadFailuresPreserveInstalledFile(t *testing.T) {
	for _, kind := range []string{"http", "html", "checksum", "truncated", "short"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "http":
					http.Error(w, "denied", 403)
				case "html":
					fmt.Fprint(w, "<html>login</html>")
				case "truncated":
					w.Header().Set("Content-Length", "10000")
					fmt.Fprint(w, "GGUF"+strings.Repeat("x", 32))
				case "short":
					fmt.Fprint(w, "GGUF")
				default:
					fmt.Fprint(w, "GGUF"+strings.Repeat("x", 32))
				}
			}))
			defer server.Close()
			m := downloadTestManager(t)
			dest := filepath.Join(filepath.Dir(m.configPath), "keep.gguf")
			os.WriteFile(dest, []byte("existing model"), 0644)
			m.download = downloadState{Active: true, Name: "keep.gguf", StartedAt: time.Now()}
			want := ""
			if kind == "checksum" {
				want = strings.Repeat("0", 64)
			}
			m.downloadURL(server.URL, dest, want)
			if m.download.Status != "failed" || m.download.Error == "" || m.download.Active {
				t.Fatalf("%#v", m.download)
			}
			got, _ := os.ReadFile(dest)
			if string(got) != "existing model" {
				t.Fatal("existing model replaced")
			}
			if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
				t.Fatal("partial file retained")
			}
		})
	}
}

func TestModelDownloadValidationAndBusy(t *testing.T) {
	m := downloadTestManager(t)
	for _, body := range []string{`{"URL":"https://","Filename":"a.gguf"}`, `{"URL":"https://example.com/a","Filename":"../a.gguf"}`, `{"URL":"https://example.com/a","Filename":"a.gguf","SHA256":"bad"}`} {
		rec := httptest.NewRecorder()
		m.handleModelDownload(rec, httptest.NewRequest("POST", "/api/models/download", strings.NewReader(body)))
		if rec.Code != 400 {
			t.Fatalf("%d %s", rec.Code, body)
		}
	}
	m.download.Active = true
	rec := httptest.NewRecorder()
	m.handleModelDownload(rec, httptest.NewRequest("POST", "/api/models/download", strings.NewReader(`{"URL":"https://example.com/a","Filename":"a.gguf"}`)))
	if rec.Code != 409 {
		t.Fatal(rec.Code)
	}
}

func TestModelDownloadLiveProgress(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "GGUF"+strings.Repeat("x", 28))
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, strings.Repeat("x", 68))
	}))
	defer server.Close()
	m := downloadTestManager(t)
	m.download = downloadState{Active: true, StartedAt: time.Now()}
	done := make(chan struct{})
	go func() {
		m.downloadURL(server.URL, filepath.Join(filepath.Dir(m.configPath), "live.gguf"), "")
		close(done)
	}()
	// Always unblock the server, including when an assertion fails.
	defer func() { close(release); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rec := httptest.NewRecorder()
		m.handleModelDownload(rec, httptest.NewRequest("GET", "/api/models/download", nil))
		var d downloadState
		json.Unmarshal(rec.Body.Bytes(), &d)
		if d.Written >= 32 {
			if !d.Active || d.Total != 100 || d.Status != "downloading" || d.BytesPerSecond <= 0 {
				t.Fatalf("%#v", d)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no live progress")
		}
		time.Sleep(time.Millisecond)
	}
}
