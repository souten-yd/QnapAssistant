package main

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
)

func TestMemorySnapshotReadOnlyAndValues(t *testing.T) {
    m := &manager{}
    get := httptest.NewRecorder()
    m.handleMemorySnapshot(get, httptest.NewRequest(http.MethodGet, "/api/system/memory", nil))
    if get.Code != http.StatusOK {
        t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
    }
    if get.Header().Get("Cache-Control") != "no-store" {
        t.Fatalf("missing no-store cache header")
    }
    var d struct {
        Total int64 `json:"total_bytes"`
        Available int64 `json:"available_bytes"`
        At int64 `json:"sampled_at_unix_ms"`
    }
    if err := json.Unmarshal(get.Body.Bytes(), &d); err != nil {
        t.Fatal(err)
    }
    if d.Total <= 0 || d.Available < 0 || d.Available > d.Total {
        t.Fatalf("bad memory stats: %+v", d)
    }
    if delta := time.Now().UnixMilli() - d.At; delta < 0 || delta > 30000 {
        t.Fatalf("unexpected sample age in ms: %d", delta)
    }
    post := httptest.NewRecorder()
    m.handleMemorySnapshot(post, httptest.NewRequest(http.MethodPost, "/api/system/memory", nil))
    if post.Code != http.StatusMethodNotAllowed {
        t.Fatalf("POST should be rejected: %d",post.Code)
    }
}
