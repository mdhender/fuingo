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

func TestIndexPage(t *testing.T) {
	t.Parallel()
	body := checkUploadPage(t, "/")
	for _, want := range []string{`id="drop-zone"`, `hx-trigger="change"`, `class="sr-only"`, `"drop"`, `maxSize = 64 * 1024`} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %s", want)
		}
	}
	// Browser dialogs block the page; errors are shown in the result area.
	if strings.Contains(body, "alert(") {
		t.Errorf("page uses alert")
	}
}

// checkUploadPage checks the requirements every upload page shares and
// returns the page.
func checkUploadPage(t *testing.T, path string) string {
	t.Helper()
	h, _ := newServer(t, "production")
	w := serve(h, httptest.NewRequest(http.MethodGet, publicOrigin+path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", path, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{
		`hx-post="/upload"`,
		`hx-encoding="multipart/form-data"`,
		`hx-target="#result"`,
		`accept=".txt"`,
		`<script src="` + server.HTMXPath + `"></script>`,
		`<script src="` + server.TailwindURL + `"></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %s", want)
		}
	}
	// The file picker is the only field, and it starts enabled.
	if regexp.MustCompile(`<input id="file"[^>]* disabled`).MatchString(body) {
		t.Errorf("file input is disabled initially")
	}
	if n := strings.Count(body, "<input "); n != 1 {
		t.Errorf("page has %d inputs, want only the file picker", n)
	}
	// Tailwind is pinned to an exact version; HTMX is never loaded from a CDN.
	if !regexp.MustCompile(`@tailwindcss/browser@\d+\.\d+\.\d+/`).MatchString(server.TailwindURL) {
		t.Errorf("Tailwind URL is not pinned: %s", server.TailwindURL)
	}
	if strings.Contains(body, "unpkg.com") || regexp.MustCompile(`https?://[^"]*htmx`).MatchString(body) {
		t.Errorf("page loads HTMX from a CDN")
	}
	if !strings.Contains(body, `responseHandling`) {
		t.Errorf("page does not swap error responses")
	}
	return body
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
