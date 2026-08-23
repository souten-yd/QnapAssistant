package main

import (
	"io"
	"net/http"
)

func (m *manager) handleUpdateUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := injectUpdateUI(renderedIndexHTML())
	html = injectOpenRouterBillingUI(html)
	html = injectOpenRouterFallbackUI(html)
	html = injectOpenRouterVoiceHealthUI(html)
	html = injectOpenRouterModelTableUI(html)
	_, _ = io.WriteString(w, html)
}
