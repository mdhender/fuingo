// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/store"
)

// post returns a POST of a JSON body; an empty body sends none.
func post(path, token, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// decode serves r, requires the status, and decodes the response into T.
func decode[T any](t *testing.T, f *fixture, r *http.Request, status int) T {
	t.Helper()
	w := f.serve(r)
	if w.Code != status {
		t.Fatalf("%s %s: %d %s, want %d", r.Method, r.URL.Path, w.Code, w.Body, status)
	}
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// players returns the emails of the game's active players.
func (f *fixture) players(t *testing.T, game store.GameRef) []string {
	t.Helper()
	emails, err := f.s.Players(t.Context(), game)
	if err != nil {
		t.Fatal(err)
	}
	return emails
}

func TestListPlayers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	if err := f.s.SetPlayerActive(t.Context(), store.Member{Game: game01, Email: spoonbill.Email}, false); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetAccountActive(t.Context(), catbird.Email, false); err != nil {
		t.Fatal(err)
	}
	list := decode[api.PlayerList](t, f, get(game01Players, token), http.StatusOK)
	if list.System != "system01" || list.Game != "game01" || len(list.Players) != 2 {
		t.Fatalf("players: %+v", list)
	}
	a, b := list.Players[0], list.Players[1]
	if a.Email != catbird.Email || !a.Active || a.AccountActive || a.ID <= 0 {
		t.Errorf("first: %+v", a)
	}
	if b.Email != spoonbill.Email || b.Active || !b.AccountActive || b.ID <= a.ID {
		t.Errorf("second: %+v", b)
	}
	if w := f.serve(get(game01Players, "")); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
}

func TestAddPlayer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)

	added := decode[api.NewPlayer](t, f, post(game01Players, token, `{"email":"Wren@Example.com"}`), http.StatusCreated)
	if added.System != "system01" || added.Game != "game01" || added.Email != "wren@example.com" || added.ID <= 0 {
		t.Fatalf("added: %+v", added)
	}
	if !slices.Contains(f.players(t, game01), "wren@example.com") {
		t.Error("the new player is not an active player of the game")
	}
	// The new account has no password, so it cannot log in.
	acct, ok, err := f.s.FindAccount(t.Context(), "wren@example.com")
	if err != nil || !ok || acct.HashedSecret != nil || !acct.Active {
		t.Errorf("account: %+v %v %v", acct, ok, err)
	}

	// An existing account in another game becomes a separate player here.
	again := decode[api.NewPlayer](t, f, post(game01Players, token, `{"email":"`+caracara.Email+`"}`), http.StatusCreated)
	if again.ID <= added.ID {
		t.Errorf("existing account: player ID %d, want one after %d", again.ID, added.ID)
	}
	if got := f.players(t, game02); !slices.Equal(got, []string{caracara.Email}) {
		t.Errorf("adding to game01 changed game02's players to %v", got)
	}
	// White space around the object is fine.
	decode[api.NewPlayer](t, f, post(game01Players, token, " {\"email\":\"padded@example.com\"}\r\n\t "), http.StatusCreated)

	for name, tc := range map[string]struct {
		token, body string
		status      int
	}{
		"duplicate":        {token, `{"email":"wren@example.com"}`, http.StatusConflict},
		"duplicate, case":  {token, `{"email":"CATBIRD@example.com"}`, http.StatusConflict},
		"empty":            {token, `{"email":""}`, http.StatusBadRequest},
		"missing":          {token, `{}`, http.StatusBadRequest},
		"space":            {token, `{"email":"a b@example.com"}`, http.StatusBadRequest},
		"non-ASCII":        {token, `{"email":"café@example.com"}`, http.StatusBadRequest},
		"given password":   {token, `{"email":"x@example.com","password":"mine"}`, http.StatusBadRequest},
		"given token":      {token, `{"email":"x@example.com","token":"mine"}`, http.StatusBadRequest},
		"not JSON":         {token, `email=x@example.com`, http.StatusBadRequest},
		"trailing data":    {token, `{"email":"x@example.com"} junk`, http.StatusBadRequest},
		"two objects":      {token, `{"email":"x@example.com"}{"email":"y@example.com"}`, http.StatusBadRequest},
		"an array":         {token, `[{"email":"x@example.com"}]`, http.StatusBadRequest},
		"wrong type":       {token, `{"email":5}`, http.StatusBadRequest},
		"no token":         {"", `{"email":"x@example.com"}`, http.StatusUnauthorized},
		"not a real token": {"nope", `{"email":"x@example.com"}`, http.StatusUnauthorized},
	} {
		if w := f.serve(post(game01Players, tc.token, tc.body)); w.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, tc.status)
		}
	}
	r := post(game01Players, token, `{"email":"x@example.com"}`)
	r.Header.Set("Content-Type", "text/plain")
	if w := f.serve(r); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", w.Code)
	}
	for _, email := range []string{"x@example.com", "y@example.com"} {
		if _, ok, _ := f.s.FindAccount(t.Context(), email); ok {
			t.Errorf("a refused request created the account %s", email)
		}
	}
}

func TestDisableAndEnablePlayer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	list := decode[api.PlayerList](t, f, get(game01Players, token), http.StatusOK)
	base := game01Players + "/" + jsonInt(list.Players[0].ID) // catbird

	for range 2 { // repeating succeeds
		st := decode[api.PlayerStatus](t, f, post(base+"/disable", token, ""), http.StatusOK)
		if st.Active || st.ID != list.Players[0].ID || st.System != "system01" || st.Game != "game01" {
			t.Errorf("disable: %+v", st)
		}
	}
	// The other player of the game is untouched.
	if got := f.players(t, game01); !slices.Equal(got, []string{spoonbill.Email}) {
		t.Errorf("players after disabling catbird = %v", got)
	}
	// The account itself is still active: only its player in this game is not.
	if acct, _, _ := f.s.FindAccount(t.Context(), catbird.Email); !acct.Active {
		t.Error("disabling a player disabled its account")
	}
	for range 2 {
		if st := decode[api.PlayerStatus](t, f, post(base+"/enable", token, ""), http.StatusOK); !st.Active {
			t.Errorf("enable: %+v", st)
		}
	}
	if got := f.players(t, game01); !slices.Equal(got, []string{catbird.Email, spoonbill.Email}) {
		t.Errorf("players after enabling catbird = %v", got)
	}
}

func TestPlayerScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	other := decode[api.PlayerList](t, f, get(game02Players, f.login(t, acct02.Email, acct02.Password)), http.StatusOK)
	otherID := jsonInt(other.Players[0].ID) // caracara, in game02

	var first string
	check := func(r *http.Request) {
		t.Helper()
		w := f.serve(r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", r.Method, r.URL.Path, w.Code)
		}
		if first == "" {
			first = w.Body.String()
		} else if w.Body.String() != first {
			t.Errorf("%s %s: body %q differs from %q", r.Method, r.URL.Path, w.Body, first)
		}
	}
	const unknown = "/api/systems/system01/games/nope/players"
	check(get(game02Players, token))
	check(get(unknown, token))
	check(post(game02Players, token, `{"email":"wren@example.com"}`))
	check(post(unknown, token, `{"email":"wren@example.com"}`))
	for _, action := range []string{"disable", "enable"} {
		check(post(game02Players+"/"+otherID+"/"+action, token, "")) // someone else's game
		check(post(game01Players+"/"+otherID+"/"+action, token, "")) // their player through a managed game
		check(post(game01Players+"/999/"+action, token, ""))
		check(post(game01Players+"/abc/"+action, token, ""))
		check(post(game01Players+"/0/"+action, token, ""))
		check(post(game01Players+"/"+strings.Repeat("9", 30)+"/"+action, token, ""))
	}
	if got := f.players(t, game02); !slices.Equal(got, []string{caracara.Email}) {
		t.Errorf("another game's players were changed to %v", got)
	}
	if _, ok, _ := f.s.FindAccount(t.Context(), "wren@example.com"); ok {
		t.Error("a refused request created an account")
	}

	// A disabled manager loses the routes; an inactive game does not.
	if err := f.s.SetGameActive(t.Context(), game01, false); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(post(game01Players, token, `{"email":"wren@example.com"}`)); w.Code != http.StatusCreated {
		t.Errorf("inactive game: %d", w.Code)
	}
	if err := f.s.SetManagerActive(t.Context(), store.Member{Game: game01, Email: acct01.Email}, false); err != nil {
		t.Fatal(err)
	}
	check(get(game01Players, token))
	check(post(game01Players, token, `{"email":"lark@example.com"}`))
}

func TestListGames(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// acct01 also manages an inactive game in another system, with the code
	// of the one it manages in system01.
	other := store.GameRef{System: "alpha", Code: "game01"}
	if err := f.s.CreateRows(t.Context(), store.Rows{
		Systems:  []string{"alpha"},
		Games:    []store.GameRef{other},
		Managers: []store.Member{{Game: other, Email: acct01.Email}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetGameActive(t.Context(), other, false); err != nil {
		t.Fatal(err)
	}
	token := f.login(t, acct01.Email, acct01.Password)
	list := decode[api.GameList](t, f, get("/api/games", token), http.StatusOK)
	want := []api.ManagedGame{{System: "alpha", Code: "game01", Active: false}, {System: "system01", Code: "game01", Active: true}}
	if !slices.Equal(list.Games, want) {
		t.Errorf("games = %+v, want %+v", list.Games, want)
	}
	// A disabled manager row drops the game; the list is then [], not null.
	for _, game := range []store.GameRef{game01, other} {
		if err := f.s.SetManagerActive(t.Context(), store.Member{Game: game, Email: acct01.Email}, false); err != nil {
			t.Fatal(err)
		}
	}
	if w := f.serve(get("/api/games", token)); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"games":[]}` {
		t.Errorf("no games: %d %s", w.Code, w.Body)
	}
	if w := f.serve(get("/api/games", "")); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
}

// Games are created only by the administrator, on the command line.
func TestGamesCannotBeCreated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	for _, path := range []string{"/api/games", "/api/systems/system01/games", "/api/systems"} {
		if w := f.serve(post(path, token, `{"code":"newgame"}`)); w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404 or 405", path, w.Code)
		}
	}
	if games, _ := f.s.Games(t.Context()); len(games) != 2 {
		t.Errorf("games = %v, want the two defaults", games)
	}
}

// Every error is JSON, including requests that match no route.
func TestUnmatchedRequestsGetJSONErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	for name, tc := range map[string]struct {
		method, path string
		status       int
		allow        string
	}{
		"unknown path":           {http.MethodGet, "/api/nope", http.StatusNotFound, ""},
		"unknown path, POST":     {http.MethodPost, game01Path + "/nope", http.StatusNotFound, ""},
		"the prefix itself":      {http.MethodGet, "/api/", http.StatusNotFound, ""},
		"a removed route":        {http.MethodGet, "/api/games/game01/uploads", http.StatusNotFound, ""},
		"wrong method":           {http.MethodDelete, game01Players, http.StatusMethodNotAllowed, "GET, HEAD, POST"},
		"GET of a POST route":    {http.MethodGet, "/api/login", http.StatusMethodNotAllowed, "POST"},
		"GET of an action":       {http.MethodGet, game01Players + "/1/disable", http.StatusMethodNotAllowed, "POST"},
		"wrong method, no token": {http.MethodPut, "/api/games", http.StatusMethodNotAllowed, "GET, HEAD"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if !strings.HasSuffix(name, "no token") {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := f.serve(r)
		var e api.Error
		err := json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != tc.status || err != nil || e.Error == "" {
			t.Errorf("%s: %d %q, want %d and a JSON error", name, w.Code, w.Body, tc.status)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type %q", name, ct)
		}
		if got := w.Header().Get("Allow"); got != tc.allow {
			t.Errorf("%s: Allow %q, want %q", name, got, tc.allow)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: no Cache-Control", name)
		}
	}
	// An unknown path is indistinguishable from an unmanaged game.
	a, b := f.serve(get("/api/nope", token)), f.serve(get(game02Players, token))
	if a.Body.String() != b.Body.String() {
		t.Errorf("404 bodies differ: %q %q", a.Body, b.Body)
	}
}
