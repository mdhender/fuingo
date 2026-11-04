// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/store"
)

const pepper = "test-pepper"

var (
	acct01   = devdata.Accounts[0] // manages game01
	acct02   = devdata.Accounts[1] // manages game02
	catbird  = devdata.Uploaders[0]
	caracara = devdata.Uploaders[2]
)

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	h     http.Handler
	s     *store.Store
	clock *clock
	logs  *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := devdata.Seed(t.Context(), s, "development", pepper); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	var logs bytes.Buffer
	h, err := api.New(api.Config{
		Store:         s,
		Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
		LoginFailures: ratelimit.New(api.FailureBurst, api.FailureInterval, c.Now),
		Now:           c.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h: h, s: s, clock: c, logs: &logs}
}

func (f *fixture) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func loginRequest(email, password, remote string) *http.Request {
	body, _ := json.Marshal(api.LoginRequest{Email: email, Password: password})
	r := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if remote != "" {
		r.RemoteAddr = remote
	}
	return r
}

func (f *fixture) login(t *testing.T, email, password string) string {
	t.Helper()
	w := f.serve(loginRequest(email, password, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", email, w.Code, w.Body)
	}
	var resp api.LoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" {
		t.Fatalf("login %s: empty token", email)
	}
	return resp.Token
}

func get(path, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// upload records an upload by a default uploader directly in the store.
func (f *fixture) upload(t *testing.T, u devdata.Uploader, turn, body string) {
	t.Helper()
	gameID, ok, err := f.s.GameID(t.Context(), u.Game)
	if err != nil || !ok {
		t.Fatalf("game %s: %v %v", u.Game, ok, err)
	}
	up, ok, err := f.s.FindUploader(t.Context(), credential.Hash(pepper, u.Email, gameID, u.Token))
	if err != nil || !ok {
		t.Fatalf("uploader %s: %v %v", u.Email, ok, err)
	}
	sum := sha256.Sum256([]byte(body))
	if err := f.s.RecordUpload(t.Context(), up.ID, up.Email, turn, hex.EncodeToString(sum[:]), []byte(body), f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Second)
}

func (f *fixture) list(t *testing.T, token, path string) api.UploadList {
	t.Helper()
	w := f.serve(get(path, token))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	var list api.UploadList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestLoginIssuesWorkingToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// The email is case-folded.
	token := f.login(t, strings.ToUpper(acct01.Email), acct01.Password)
	if w := f.serve(get("/api/games/game01/uploads", token)); w.Code != http.StatusOK {
		t.Errorf("list with token: %d %s", w.Code, w.Body)
	}
	if got := f.serve(loginRequest(acct01.Email, acct01.Password, "")).Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if strings.Contains(f.logs.String(), token) || strings.Contains(f.logs.String(), acct01.Password) {
		t.Errorf("logs contain a credential:\n%s", f.logs)
	}
}

func TestLoginFailuresAreIdentical(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.s.SetAccountActive(t.Context(), acct02.Email, false); err != nil {
		t.Fatal(err)
	}
	cases := map[string]*http.Request{
		"wrong password":      loginRequest(acct01.Email, "wrong", "192.0.2.1:1"),
		"unknown email":       loginRequest("nobody@example.com", acct01.Password, "192.0.2.2:1"),
		"account no password": loginRequest(catbird.Email, catbird.Token, "192.0.2.3:1"),
		"inactive account":    loginRequest(acct02.Email, acct02.Password, "192.0.2.4:1"),
		"password over 72":    loginRequest(acct01.Email, acct01.Password+strings.Repeat("x", 80), "192.0.2.5:1"),
	}
	var first string
	for name, r := range cases {
		w := f.serve(r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, w.Code)
		}
		if first == "" {
			first = w.Body.String()
		} else if w.Body.String() != first {
			t.Errorf("%s: body %q differs from %q", name, w.Body, first)
		}
	}
	if strings.Contains(f.logs.String(), "wrong") || strings.Contains(f.logs.String(), acct02.Password) {
		t.Errorf("logs contain a password:\n%s", f.logs)
	}
}

func TestLoginRejectsMalformedRequests(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const remote = "192.0.2.9:1"
	for i := range api.FailureBurst + 2 {
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader("{"))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = remote
		if w := f.serve(r); w.Code != http.StatusBadRequest {
			t.Errorf("bad JSON %d: status %d", i, w.Code)
		}
	}
	r := loginRequest(acct01.Email, acct01.Password, remote)
	r.Header.Set("Content-Type", "text/plain")
	if w := f.serve(r); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: status %d", w.Code)
	}
	// Malformed requests do not count as failed logins.
	if w := f.serve(loginRequest(acct01.Email, acct01.Password, remote)); w.Code != http.StatusOK {
		t.Errorf("login after malformed requests: %d %s", w.Code, w.Body)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if w := f.serve(httptest.NewRequest(method, "/api/login", nil)); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/login: status %d, want 405", method, w.Code)
		}
	}
}

func TestLoginFailureLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const remote = "192.0.2.10:1"
	// Successes do not count.
	for range api.FailureBurst + 1 {
		if w := f.serve(loginRequest(acct01.Email, acct01.Password, remote)); w.Code != http.StatusOK {
			t.Fatalf("login: %d", w.Code)
		}
	}
	for i := range api.FailureBurst {
		if w := f.serve(loginRequest(acct01.Email, "wrong", remote)); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d", i, w.Code)
		}
	}
	// Valid credentials are refused while limited.
	w := f.serve(loginRequest(acct01.Email, acct01.Password, remote))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Errorf("limited login: %d Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	// Another client, with the same email, is unaffected.
	if w := f.serve(loginRequest(acct01.Email, acct01.Password, "192.0.2.11:1")); w.Code != http.StatusOK {
		t.Errorf("other client: %d", w.Code)
	}
	f.clock.Advance(api.FailureInterval)
	if w := f.serve(loginRequest(acct01.Email, acct01.Password, remote)); w.Code != http.StatusOK {
		t.Errorf("after one interval: %d", w.Code)
	}
}

func TestBearerAuthentication(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	const path = "/api/games/game01/uploads"
	cases := map[string]string{
		"missing":      "",
		"basic scheme": "Basic " + token,
		"empty bearer": "Bearer ",
		"unknown":      "Bearer not-a-token",
		"upload token": "Bearer " + catbird.Token,
		"password":     "Bearer " + acct01.Password,
	}
	for name, header := range cases {
		r := get(path, "")
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		w := f.serve(r)
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: status %d, WWW-Authenticate %q", name, w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
	// The scheme is case-insensitive.
	r := get(path, "")
	r.Header.Set("Authorization", "bearer "+token)
	if w := f.serve(r); w.Code != http.StatusOK {
		t.Errorf("lowercase scheme: %d", w.Code)
	}
}

func TestTokenExpires(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	f.clock.Advance(api.SessionLifetime - time.Second)
	if w := f.serve(get("/api/games/game01/uploads", token)); w.Code != http.StatusOK {
		t.Errorf("before expiry: %d", w.Code)
	}
	f.clock.Advance(time.Second)
	if w := f.serve(get("/api/games/game01/uploads", token)); w.Code != http.StatusUnauthorized {
		t.Errorf("at expiry: %d", w.Code)
	}
}

func TestDisabledAccountTokenStopsWorking(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	if err := f.s.SetAccountActive(t.Context(), acct01.Email, false); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(get("/api/games/game01/uploads", token)); w.Code != http.StatusUnauthorized {
		t.Errorf("disabled account: %d", w.Code)
	}
}

func TestListUploads(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	spoonbill := devdata.Uploaders[1]
	f.upload(t, catbird, "3", "begin 1\nend\n")
	f.upload(t, spoonbill, "3", "begin 2\nend\n")
	f.upload(t, catbird, "03", "begin 1 late\nend\n")
	f.upload(t, catbird, "3", "begin 1\nend\n") // identical content again
	f.upload(t, caracara, "3", "begin 9\nend\n")
	token := f.login(t, acct01.Email, acct01.Password)

	list := f.list(t, token, "/api/games/GAME01/uploads?turn=3")
	if list.Game != "game01" {
		t.Errorf("game = %q", list.Game)
	}
	var emails []string
	for i, u := range list.Uploads {
		emails = append(emails, u.Email)
		if u.Turn != "3" {
			t.Errorf("upload %d turn %q", i, u.Turn)
		}
		if i > 0 && u.ID <= list.Uploads[i-1].ID {
			t.Errorf("uploads out of order: %+v", list.Uploads)
		}
		if _, err := time.Parse(api.TimeLayout, u.UploadedAt); err != nil {
			t.Errorf("uploaded_at %q: %v", u.UploadedAt, err)
		}
	}
	if want := []string{catbird.Email, spoonbill.Email, catbird.Email}; strings.Join(emails, ",") != strings.Join(want, ",") {
		t.Errorf("emails = %v, want %v", emails, want)
	}
	if list.Uploads[0].ContentHash != list.Uploads[2].ContentHash || list.Uploads[0].Size != int64(len("begin 1\nend\n")) {
		t.Errorf("uploads = %+v", list.Uploads)
	}
	if all := f.list(t, token, "/api/games/game01/uploads"); len(all.Uploads) != 4 {
		t.Errorf("unfiltered: %d uploads, want 4", len(all.Uploads))
	}
	if none := f.list(t, token, "/api/games/game01/uploads?turn=4"); none.Uploads == nil || len(none.Uploads) != 0 {
		t.Errorf("turn 4: %+v", none.Uploads)
	}
	if w := f.serve(get("/api/games/game01/uploads?turn=", token)); w.Code != http.StatusBadRequest {
		t.Errorf("empty turn: %d", w.Code)
	}
}

func TestGameScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.upload(t, catbird, "1", "same body\n")
	f.upload(t, caracara, "1", "same body\n") // shares catbird's blob
	token := f.login(t, acct01.Email, acct01.Password)
	game01 := f.list(t, token, "/api/games/game01/uploads")
	other := f.list(t, f.login(t, acct02.Email, acct02.Password), "/api/games/game02/uploads")
	if len(game01.Uploads) != 1 || len(other.Uploads) != 1 {
		t.Fatalf("uploads: %+v %+v", game01, other)
	}
	otherID := jsonInt(other.Uploads[0].ID)

	notFound := []string{
		"/api/games/game02/uploads",                                      // managed by someone else
		"/api/games/nope/uploads",                                        // unknown
		"/api/games/game02/uploads/" + otherID + "/body",                 // other game's upload
		"/api/games/game01/uploads/" + otherID + "/body",                 // other game's upload through a managed game
		"/api/games/game01/uploads/999/body",                             // unknown upload
		"/api/games/game01/uploads/abc/body",                             // not an ID
		"/api/games/game01/uploads/0/body",                               // not an ID
		"/api/games/game01/uploads/" + strings.Repeat("9", 30) + "/body", // overflow
	}
	var first string
	for _, path := range notFound {
		w := f.serve(get(path, token))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, w.Code)
		}
		if first == "" {
			first = w.Body.String()
		} else if w.Body.String() != first {
			t.Errorf("%s: body %q differs from %q", path, w.Body, first)
		}
	}
	// There is no route by content hash.
	if w := f.serve(get("/api/blobs/"+game01.Uploads[0].ContentHash, token)); w.Code != http.StatusNotFound {
		t.Errorf("blob by hash: %d", w.Code)
	}

	// A disabled manager row loses access; an inactive game does not.
	if err := f.s.SetGameActive(t.Context(), "game01", false); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(get("/api/games/game01/uploads", token)); w.Code != http.StatusOK {
		t.Errorf("inactive game: %d", w.Code)
	}
	if err := f.s.SetManagerActive(t.Context(), "game01", acct01.Email, false); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(get("/api/games/game01/uploads", token)); w.Code != http.StatusNotFound {
		t.Errorf("disabled manager: %d", w.Code)
	}
}

func TestUploadBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const body = "begin 1 \"pw\"\r\nunit 2\r\nend\r\n"
	f.upload(t, catbird, "1", body)
	token := f.login(t, acct01.Email, acct01.Password)
	u := f.list(t, token, "/api/games/game01/uploads").Uploads[0]
	w := f.serve(get("/api/games/game01/uploads/"+jsonInt(u.ID)+"/body", token))
	if w.Code != http.StatusOK {
		t.Fatalf("body: %d %s", w.Code, w.Body)
	}
	got, _ := io.ReadAll(w.Body)
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if h := w.Header().Get(api.HeaderContentSHA256); h != u.ContentHash {
		t.Errorf("%s = %q, want %q", api.HeaderContentSHA256, h, u.ContentHash)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", w.Header())
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
