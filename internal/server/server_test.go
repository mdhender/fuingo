// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/server"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	// publicHost is the browser-facing origin that Caddy forwards.
	publicHost   = "htmx-app.localhost:8443"
	publicOrigin = "https://" + publicHost
)

var catbird = devdata.Players[0]

func newServer(t *testing.T, env string) (http.Handler, *store.Store) {
	t.Helper()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name()+"/"+env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := devdata.Seed(t.Context(), s, "development"); err != nil {
		t.Fatal(err)
	}
	h, err := server.New(server.Config{
		Env:    env,
		Store:  s,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, s
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The server accepts no files until the new design's uploads are built: the
// previous design's form is gone, and so is its helper.
func TestThereIsNoUploadRoute(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"production", "development"} {
		h, _ := newServer(t, env)
		for _, path := range []string{"/upload", "/__development/create-account/game01/x@example.com/token"} {
			r := httptest.NewRequest(http.MethodPost, publicOrigin+path, strings.NewReader("game01 1 "+catbird.Email+" token\norders\n"))
			r.Header.Set("Origin", publicOrigin)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			if w := serve(h, r); w.Code != http.StatusNotFound {
				t.Errorf("%s: POST %s: status %d, want 404", env, path, w.Code)
			}
		}
	}
}

func TestSameOriginRequestsPass(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	// A browser's same-origin request, with Fetch Metadata.
	r := apiLogin(acct01.Email, acct01.Password)
	r.Header.Set("Origin", publicOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := serve(h, r); w.Code != http.StatusOK {
		t.Errorf("same-origin with Fetch Metadata: %d %s", w.Code, w.Body)
	}
	// A browser without Fetch Metadata: Origin is compared with Host,
	// including the port.
	r = apiLogin(acct01.Email, acct01.Password)
	r.Header.Set("Origin", publicOrigin)
	if w := serve(h, r); w.Code != http.StatusOK {
		t.Errorf("same-origin with Origin only: %d %s", w.Code, w.Body)
	}
}

func TestCrossOriginRequestsAreRejected(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	cases := map[string]func(*http.Request){
		"cross-site fetch metadata": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		},
		"same-site fetch metadata": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-site")
		},
		"origin with another host": func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.localhost:8443")
		},
		"origin with another port": func(r *http.Request) {
			r.Header.Set("Origin", "https://htmx-app.localhost:18443")
		},
		"backend host with public origin": func(r *http.Request) {
			r.Header.Set("Origin", publicOrigin)
			r.Host = "127.0.0.1:18443"
		},
		"HX-Request is not trusted": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("HX-Request", "true")
		},
	}
	for name, modify := range cases {
		r := apiLogin(acct01.Email, acct01.Password)
		modify(r)
		if w := serve(h, r); w.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, w.Code)
		}
	}
}

func TestUnknownRoutes(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "development")
	for _, path := range []string{"/__development/create-api-account/x/y", "/__development/log-me-in", "/api/upload", "/nope"} {
		r := httptest.NewRequest(http.MethodPost, publicOrigin+path, strings.NewReader(""))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if w := serve(h, r); w.Code != http.StatusNotFound {
			t.Errorf("POST %s: status %d, want 404", path, w.Code)
		}
	}
}
