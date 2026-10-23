// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"bytes"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/server"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	pepper = "test-pepper"
	// publicHost is the browser-facing origin that Caddy forwards.
	publicHost   = "htmx-app.localhost:8443"
	publicOrigin = "https://" + publicHost
)

var catbird = devdata.Uploaders[0]

func newServer(t *testing.T, env string) (http.Handler, *store.Store) {
	t.Helper()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name()+"/"+env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := devdata.Seed(t.Context(), s, "development", pepper); err != nil {
		t.Fatal(err)
	}
	h, err := server.New(server.Config{
		Env:       env,
		Store:     s,
		Pepper:    pepper,
		UploadDir: t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
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

// uploadRequest builds a same-origin upload through the public origin, with
// the headers a browser sends.
func uploadRequest(t *testing.T, email, token, file string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("email", email)
	mw.WriteField("token", token)
	fw, _ := mw.CreateFormFile("file", "turn.txt")
	io.WriteString(fw, file)
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, publicOrigin+"/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", publicOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("HX-Request", "true")
	return r
}

func helperRequest(method, prefix, game, email, token string) *http.Request {
	path := prefix + "/create-account/" + url.PathEscape(game) + "/" + url.PathEscape(email) + "/" + url.PathEscape(token)
	r := httptest.NewRequest(method, publicOrigin+path, nil)
	r.Header.Set("Origin", publicOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

func TestSameOriginUploads(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	// HTMX request with Fetch Metadata.
	if w := serve(h, uploadRequest(t, catbird.Email, catbird.Token, "turn\n")); w.Code != http.StatusOK {
		t.Errorf("same-origin HTMX upload: %d %s", w.Code, w.Body)
	}
	// A browser without Fetch Metadata: Origin is compared with Host,
	// including the port.
	r := uploadRequest(t, catbird.Email, catbird.Token, "turn 2\n")
	r.Header.Del("Sec-Fetch-Site")
	r.Header.Del("HX-Request")
	if w := serve(h, r); w.Code != http.StatusOK {
		t.Errorf("same-origin upload with Origin only: %d %s", w.Code, w.Body)
	}
}

func TestCrossOriginUploadsAreRejected(t *testing.T) {
	t.Parallel()
	h, s := newServer(t, "production")
	cases := map[string]func(*http.Request){
		"cross-site fetch metadata": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		},
		"same-site fetch metadata": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-site")
		},
		"origin with another host": func(r *http.Request) {
			r.Header.Del("Sec-Fetch-Site")
			r.Header.Set("Origin", "https://evil.localhost:8443")
		},
		"origin with another port": func(r *http.Request) {
			r.Header.Del("Sec-Fetch-Site")
			r.Header.Set("Origin", "https://htmx-app.localhost:18443")
		},
		"backend host with public origin": func(r *http.Request) {
			r.Header.Del("Sec-Fetch-Site")
			r.Host = "127.0.0.1:18443"
		},
		"HX-Request is not trusted": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("HX-Request", "true")
		},
	}
	for name, modify := range cases {
		r := uploadRequest(t, catbird.Email, catbird.Token, "turn\n")
		modify(r)
		if w := serve(h, r); w.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, w.Code)
		}
	}
	if uploads, _ := s.Uploads(t.Context()); len(uploads) != 0 {
		t.Errorf("%d uploads recorded from cross-origin requests", len(uploads))
	}
}

func TestUploadRequiresPost(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
		r := httptest.NewRequest(method, publicOrigin+"/upload", nil)
		if w := serve(h, r); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /upload: status %d, want 405", method, w.Code)
		}
	}
}

func TestHelperPrefixIsolation(t *testing.T) {
	t.Parallel()
	for env, available := range map[string]string{"development": "/__development", "agent": "/__agent", "test": "", "production": ""} {
		h, _ := newServer(t, env)
		for i, prefix := range []string{"/__development", "/__agent"} {
			email := "new" + string(rune('a'+i)) + "@example.com"
			w := serve(h, helperRequest(http.MethodPost, prefix, "game01", email, "token-"+email))
			want := http.StatusNotFound
			if prefix == available {
				want = http.StatusCreated
			}
			if w.Code != want {
				t.Errorf("%s: POST %s: status %d, want %d", env, prefix, w.Code, want)
			}
		}
	}
}

func TestHelperCreatesAccountThatCanUpload(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"development", "agent"} {
		h, s := newServer(t, env)
		prefix := server.HelperPrefix(env)
		// Mixed case and characters that need escaping in a path segment.
		w := serve(h, helperRequest(http.MethodPost, prefix, "game02", "Heron+Test@Example.com", "tok/en?#%"))
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: create-account: %d %s", env, w.Code, w.Body)
		}
		if w := serve(h, uploadRequest(t, "heron+test@example.com", "tok/en?#%", "turn\n")); w.Code != http.StatusOK {
			t.Errorf("%s: upload with helper account: %d %s", env, w.Code, w.Body)
		}
		uploads, _ := s.Uploads(t.Context())
		if len(uploads) != 1 || uploads[0].GameCode != "game02" || uploads[0].Email != "heron+test@example.com" {
			t.Errorf("%s: uploads = %+v", env, uploads)
		}
	}
}

func TestHelperRefusesUnknownGame(t *testing.T) {
	t.Parallel()
	h, s := newServer(t, "development")
	if w := serve(h, helperRequest(http.MethodPost, "/__development", "game99", "x@example.com", "t")); w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
	if games, _ := s.Games(t.Context()); len(games) != len(devdata.Games) {
		t.Errorf("games = %v; a game was created", games)
	}
}

func TestHelperRequiresPost(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "development")
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut} {
		if w := serve(h, helperRequest(method, "/__development", "game01", "x@example.com", "t")); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", method, w.Code)
		}
	}
	// None of them created the account.
	if w := serve(h, uploadRequest(t, "x@example.com", "t", "turn\n")); w.Code != http.StatusForbidden {
		t.Errorf("upload after non-POST helpers: %d, want 403", w.Code)
	}
}

func TestHelperDuplicatesDoNotModify(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "development")
	// An existing default account, with a new token.
	if w := serve(h, helperRequest(http.MethodPost, "/__development", catbird.Game, "CATBIRD@example.com", "new-token")); w.Code != http.StatusConflict {
		t.Errorf("duplicate email: status %d, want 409", w.Code)
	}
	// The same email and token in another game have the same credential hash.
	if w := serve(h, helperRequest(http.MethodPost, "/__development", "game02", catbird.Email, catbird.Token)); w.Code != http.StatusConflict {
		t.Errorf("credential hash in use: status %d, want 409", w.Code)
	}
	if w := serve(h, uploadRequest(t, catbird.Email, catbird.Token, "turn\n")); w.Code != http.StatusOK {
		t.Errorf("original token: %d, want 200", w.Code)
	}
	if w := serve(h, uploadRequest(t, catbird.Email, "new-token", "turn\n")); w.Code != http.StatusForbidden {
		t.Errorf("rejected token: %d, want 403", w.Code)
	}
}

func TestHelperConcurrentDuplicates(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "development")
	codes := make(chan int, 10)
	for i := range 10 {
		go func() {
			token := "token-" + string(rune('a'+i))
			codes <- serve(h, helperRequest(http.MethodPost, "/__development", "game01", "race@example.com", token)).Code
		}()
	}
	created := 0
	for range 10 {
		switch code := <-codes; code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("status %d", code)
		}
	}
	if created != 1 {
		t.Errorf("%d racing requests created the account, want 1", created)
	}
}

func TestHelperCrossOriginIsRejected(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "development")
	r := helperRequest(http.MethodPost, "/__development", "game01", "x@example.com", "t")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := serve(h, r); w.Code != http.StatusForbidden {
		t.Errorf("cross-site: status %d, want 403", w.Code)
	}
	r = helperRequest(http.MethodPost, "/__development", "game01", "x@example.com", "t")
	r.Header.Del("Sec-Fetch-Site")
	r.Header.Set("Origin", "https://evil.example")
	if w := serve(h, r); w.Code != http.StatusForbidden {
		t.Errorf("mismatched origin: status %d, want 403", w.Code)
	}
	if w := serve(h, uploadRequest(t, "x@example.com", "t", "turn\n")); w.Code != http.StatusForbidden {
		t.Errorf("a cross-origin helper request created the account")
	}
}

func TestUnknownRoutes(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "development")
	for _, path := range []string{"/__development/create-account/game01/x", "/__development/log-me-in/x", "/api/upload", "/nope"} {
		r := httptest.NewRequest(http.MethodPost, publicOrigin+path, strings.NewReader(""))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if w := serve(h, r); w.Code != http.StatusNotFound {
			t.Errorf("POST %s: status %d, want 404", path, w.Code)
		}
	}
}
