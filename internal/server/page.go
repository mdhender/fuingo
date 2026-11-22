// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"time"
)

const (
	// HTMXPath serves the vendored HTMX release. The version in the name lets
	// browsers cache it forever.
	HTMXPath = "/static/htmx-2.0.11.min.js"
	// TailwindURL loads the Tailwind CSS browser build, pinned to one release.
	TailwindURL = "https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"
)

//go:embed static/htmx-2.0.11.min.js
var htmxJS []byte

//go:embed templates/*.html
var templates embed.FS

var pageTemplates = template.Must(template.ParseFS(templates, "templates/*.html"))

// startTime is the modification time reported for embedded files.
var startTime = time.Now()

func serveHTMX(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", startTime, bytes.NewReader(htmxJS))
}
