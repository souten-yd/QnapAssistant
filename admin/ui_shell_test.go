package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShellWrapsEveryCardAndKeepsExistingUI(t *testing.T) {
	m := &manager{}
	rec := httptest.NewRecorder()
	m.handleUpdateUI(rec, httptest.NewRequest("GET", "/", nil))
	html := rec.Body.String()
	for _, c := range shellCardMap {
		if n := strings.Count(html, "<h3>"+c.Heading+"</h3>"); n != 1 {
			t.Errorf("card heading %q appears %d times; update shellCardMap", c.Heading, n)
		}
	}
	if got := strings.Count(html, "<h3>") - strings.Count(shellHTML, "<h3>"); got != len(shellCardMap) {
		t.Errorf("page has %d cards but shellCardMap maps %d; new cards need a tab", got, len(shellCardMap))
	}
	for _, id := range []string{`id="updateCard"`, `id="logs"`, `id="status"`, `id="dlpreset"`, `id="orKey"`, `data-panel="home"`, `data-panel="debug"`, `id="qaChecks"`} {
		if !strings.Contains(html, id) {
			t.Errorf("missing %s", id)
		}
	}
	if strings.Contains(html, "__CARD_MAP__") || strings.Count(html, `id="qa-shell-style"`) != 1 {
		t.Error("shell injected incorrectly")
	}
	// The shell script must run after every other script (it calls act(), logs() …).
	last := strings.LastIndex(html, "<script>")
	if !strings.Contains(html[last:], "qaShowTab") {
		t.Error("shell script is not the last script")
	}
	if injectShellUI(html) != html {
		t.Error("injectShellUI must be idempotent")
	}
}
