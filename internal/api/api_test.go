// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/store"
)

// Paths of the default games and their players.
const (
	game01Path    = "/api/systems/system01/games/game01"
	game02Path    = "/api/systems/system01/games/game02"
	game01Players = game01Path + "/players"
	game02Players = game02Path + "/players"
)

var (
	acct01    = devdata.Accounts[0] // manages game01
	acct02    = devdata.Accounts[1] // manages game02
	catbird   = devdata.Players[0]  // plays game01
	spoonbill = devdata.Players[1]  // plays game01
	caracara  = devdata.Players[2]  // plays game02

	game01 = store.GameRef{System: devdata.System, Code: "game01"}
	game02 = store.GameRef{System: devdata.System, Code: "game02"}
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
	if err := devdata.Seed(t.Context(), s, "development"); err != nil {
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

func TestLoginIssuesWorkingToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// The email is case-folded.
	token := f.login(t, strings.ToUpper(acct01.Email), acct01.Password)
	if w := f.serve(get(game01Players, token)); w.Code != http.StatusOK {
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
		"account no password": loginRequest(catbird.Email, acct01.Password, "192.0.2.3:1"),
		"email with a space":  loginRequest("acct 01@example.com", acct01.Password, "192.0.2.6:1"),
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
	// The right password followed by anything else is still malformed.
	trailing := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"email":"`+acct01.Email+`","password":"`+acct01.Password+`"} junk`))
	trailing.Header.Set("Content-Type", "application/json")
	trailing.RemoteAddr = remote
	if w := f.serve(trailing); w.Code != http.StatusBadRequest {
		t.Errorf("trailing data: status %d", w.Code)
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
	const path = game01Players
	cases := map[string]string{
		"missing":      "",
		"basic scheme": "Basic " + token,
		"empty bearer": "Bearer ",
		"unknown":      "Bearer not-a-token",
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
	if w := f.serve(get(game01Players, token)); w.Code != http.StatusOK {
		t.Errorf("before expiry: %d", w.Code)
	}
	f.clock.Advance(time.Second)
	if w := f.serve(get(game01Players, token)); w.Code != http.StatusUnauthorized {
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
	if w := f.serve(get(game01Players, token)); w.Code != http.StatusUnauthorized {
		t.Errorf("disabled account: %d", w.Code)
	}
}

func TestGameScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// The same codes in another system are other games.
	if err := f.s.CreateRows(t.Context(), store.Rows{
		Systems: []string{"system02"},
		Games:   []store.GameRef{{System: "system02", Code: "game01"}},
	}); err != nil {
		t.Fatal(err)
	}
	token := f.login(t, acct01.Email, acct01.Password)
	// Codes in the path are case-folded.
	if w := f.serve(get("/api/systems/SYSTEM01/games/GAME01/players", token)); w.Code != http.StatusOK {
		t.Errorf("uppercase codes: %d", w.Code)
	}
	var first string
	for _, path := range []string{
		game02Players, // managed by someone else
		"/api/systems/system01/games/nope/players",                            // unknown game
		"/api/systems/nope/games/game01/players",                              // unknown system
		"/api/systems/system02/games/game01/players",                          // the same code in another system
		"/api/systems/system01/games/a%20b/players",                           // not a code
		"/api/systems/system01/games/a%3Cb/players",                           // not a code
		"/api/systems/sys%2F01/games/game01/players",                          // not a code
		"/api/systems/system01/games/" + strings.Repeat("a", 33) + "/players", // too long
	} {
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

	// A disabled manager row loses access; an inactive game does not.
	if err := f.s.SetGameActive(t.Context(), game01, false); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(get(game01Players, token)); w.Code != http.StatusOK {
		t.Errorf("inactive game: %d", w.Code)
	}
	if err := f.s.SetManagerActive(t.Context(), store.Member{Game: game01, Email: acct01.Email}, false); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(get(game01Players, token)); w.Code != http.StatusNotFound {
		t.Errorf("disabled manager: %d", w.Code)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
