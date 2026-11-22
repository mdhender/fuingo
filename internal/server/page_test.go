// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/server"
)

// The page is a placeholder until signing in is built: it loads the vendored
// HTMX and the pinned Tailwind, and offers nothing to upload with.
func TestIndexPage(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	w := serve(h, httptest.NewRequest(http.MethodGet, publicOrigin+"/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<script src="` + server.HTMXPath + `"></script>`,
		`<script src="` + server.TailwindURL + `"></script>`,
		`id="status"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %s", want)
		}
	}
	for _, gone := range []string{"<form", "<input", "/upload", "alert("} {
		if strings.Contains(body, gone) {
			t.Errorf("page contains %s", gone)
		}
	}
	// Tailwind is pinned to an exact version; HTMX is never loaded from a CDN.
	if !regexp.MustCompile(`@tailwindcss/browser@\d+\.\d+\.\d+/`).MatchString(server.TailwindURL) {
		t.Errorf("Tailwind URL is not pinned: %s", server.TailwindURL)
	}
	if strings.Contains(body, "unpkg.com") || regexp.MustCompile(`https?://[^"]*htmx`).MatchString(body) {
		t.Errorf("page loads HTMX from a CDN")
	}
}

func TestOnlyRootServesThePage(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	if w := serve(h, httptest.NewRequest(http.MethodGet, publicOrigin+"/other", nil)); w.Code != http.StatusNotFound {
		t.Errorf("GET /other: %d, want 404", w.Code)
	}
}

func TestVendoredHTMX(t *testing.T) {
	t.Parallel()
	want, err := os.ReadFile("static/htmx-2.0.11.min.js")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^/static/htmx-\d+\.\d+\.\d+\.min\.js$`).MatchString(server.HTMXPath) {
		t.Errorf("HTMX path %q does not name an exact version", server.HTMXPath)
	}
	h, _ := newServer(t, "production")
	w := serve(h, httptest.NewRequest(http.MethodGet, publicOrigin+server.HTMXPath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", server.HTMXPath, w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Errorf("served HTMX differs from the vendored file")
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}
}
