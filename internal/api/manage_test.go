// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/credential"
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

// canUpload reports whether the token authorizes the email to upload to
// the game.
func (f *fixture) canUpload(t *testing.T, game, email, token string) bool {
	t.Helper()
	gameID, ok, err := f.s.GameID(t.Context(), game)
	if err != nil || !ok {
		t.Fatalf("game %s: %v %v", game, ok, err)
	}
	_, ok, err = f.s.FindUploader(t.Context(), credential.Hash(pepper, email, gameID, token))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestNewRequiresPepper(t *testing.T) {
	t.Parallel()
	if _, err := api.New(api.Config{}); err != api.ErrMissingPepper {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateGame(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)

	game := decode[api.Game](t, f, post("/api/games", token, `{"code":"NewGame"}`), http.StatusCreated)
	if game.Game != "newgame" {
		t.Errorf("game = %q", game.Game)
	}
	// The creator manages it; nobody else does.
	list := decode[api.UploaderList](t, f, get("/api/games/newgame/uploaders", token), http.StatusOK)
	if list.Game != "newgame" || list.Uploaders == nil || len(list.Uploaders) != 0 {
		t.Errorf("uploaders: %+v", list)
	}
	other := f.login(t, acct02.Email, acct02.Password)
	if w := f.serve(get("/api/games/newgame/uploaders", other)); w.Code != http.StatusNotFound {
		t.Errorf("other account: %d", w.Code)
	}
	// It is active: an uploader added to it can upload.
	added := decode[api.UploaderToken](t, f, post("/api/games/newgame/uploaders", token, `{"email":"wren@example.com"}`), http.StatusCreated)
	if !f.canUpload(t, "newgame", "wren@example.com", added.Token) {
		t.Error("uploader of the new game cannot upload")
	}

	for name, tc := range map[string]struct {
		token, body string
		status      int
	}{
		"duplicate":        {token, `{"code":"newgame"}`, http.StatusConflict},
		"duplicate, case":  {token, `{"code":"GAME02"}`, http.StatusConflict},
		"empty code":       {token, `{"code":""}`, http.StatusBadRequest},
		"missing code":     {token, `{}`, http.StatusBadRequest},
		"space in code":    {token, `{"code":"a b"}`, http.StatusBadRequest},
		"non-ASCII code":   {token, `{"code":"café"}`, http.StatusBadRequest},
		"unknown field":    {token, `{"code":"x","is_active":false}`, http.StatusBadRequest},
		"not JSON":         {token, `code=x`, http.StatusBadRequest},
		"no token":         {"", `{"code":"x"}`, http.StatusUnauthorized},
		"not a real token": {"nope", `{"code":"x"}`, http.StatusUnauthorized},
	} {
		if w := f.serve(post("/api/games", tc.token, tc.body)); w.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, tc.status)
		}
	}
	r := post("/api/games", token, `{"code":"x"}`)
	r.Header.Set("Content-Type", "text/plain")
	if w := f.serve(r); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", w.Code)
	}
	if _, ok, _ := f.s.GameID(t.Context(), "x"); ok {
		t.Error("a refused request created a game")
	}
}

func TestListUploaders(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	if err := f.s.SetUploaderActive(t.Context(), "game01", "spoonbill@example.com", false); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetAccountActive(t.Context(), catbird.Email, false); err != nil {
		t.Fatal(err)
	}
	list := decode[api.UploaderList](t, f, get("/api/games/game01/uploaders", token), http.StatusOK)
	if len(list.Uploaders) != 2 {
		t.Fatalf("uploaders: %+v", list)
	}
	a, b := list.Uploaders[0], list.Uploaders[1]
	if a.Email != catbird.Email || !a.Active || a.AccountActive || a.ID <= 0 {
		t.Errorf("first: %+v", a)
	}
	if b.Email != "spoonbill@example.com" || b.Active || !b.AccountActive || b.ID <= a.ID {
		t.Errorf("second: %+v", b)
	}
	if w := f.serve(get("/api/games/game01/uploaders", "")); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
}

func TestAddUploader(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)

	added := decode[api.UploaderToken](t, f, post("/api/games/game01/uploaders", token, `{"email":"Wren@Example.com"}`), http.StatusCreated)
	if added.Game != "game01" || added.Email != "wren@example.com" || added.ID <= 0 || len(added.Token) != 36 {
		t.Fatalf("added: game %q email %q id %d token length %d", added.Game, added.Email, added.ID, len(added.Token))
	}
	if !f.canUpload(t, "game01", "wren@example.com", added.Token) {
		t.Error("the new token does not authorize an upload")
	}
	// The new account has no API password, and the upload token is not one.
	acct, ok, err := f.s.FindAccount(t.Context(), "wren@example.com")
	if err != nil || !ok || acct.HashedSecret != nil || !acct.Active {
		t.Errorf("account: %+v %v %v", acct, ok, err)
	}
	if w := f.serve(loginRequest("wren@example.com", added.Token, "")); w.Code != http.StatusUnauthorized {
		t.Errorf("login with upload token: %d", w.Code)
	}
	if strings.Contains(f.logs.String(), added.Token) {
		t.Error("the token was logged")
	}

	// An existing account in another game gets its own credential here.
	again := decode[api.UploaderToken](t, f, post("/api/games/game01/uploaders", token, `{"email":"`+caracara.Email+`"}`), http.StatusCreated)
	if again.Token == added.Token || !f.canUpload(t, "game01", caracara.Email, again.Token) {
		t.Error("existing account: no working token of its own")
	}
	if f.canUpload(t, "game02", caracara.Email, again.Token) || !f.canUpload(t, "game02", caracara.Email, caracara.Token) {
		t.Error("adding to game01 changed the game02 credential")
	}

	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"duplicate":       {`{"email":"wren@example.com"}`, http.StatusConflict},
		"duplicate, case": {`{"email":"CATBIRD@example.com"}`, http.StatusConflict},
		"empty":           {`{"email":""}`, http.StatusBadRequest},
		"space":           {`{"email":"a b@example.com"}`, http.StatusBadRequest},
		"given token":     {`{"email":"x@example.com","token":"mine"}`, http.StatusBadRequest},
	} {
		if w := f.serve(post("/api/games/game01/uploaders", token, tc.body)); w.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, tc.status)
		}
	}
	if _, ok, _ := f.s.FindAccount(t.Context(), "x@example.com"); ok {
		t.Error("a refused request created an account")
	}
}

func TestDisableEnableAndResetUploader(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	list := decode[api.UploaderList](t, f, get("/api/games/game01/uploaders", token), http.StatusOK)
	base := "/api/games/game01/uploaders/" + jsonInt(list.Uploaders[0].ID) // catbird

	for range 2 { // repeating succeeds
		st := decode[api.UploaderStatus](t, f, post(base+"/disable", token, ""), http.StatusOK)
		if st.Active || st.ID != list.Uploaders[0].ID || st.Game != "game01" {
			t.Errorf("disable: %+v", st)
		}
	}
	if f.canUpload(t, catbird.Game, catbird.Email, catbird.Token) {
		t.Error("a disabled uploader can upload")
	}

	// A reset replaces the token and leaves the uploader disabled.
	reset := decode[api.UploaderToken](t, f, post(base+"/reset-token", token, ""), http.StatusOK)
	if reset.Email != catbird.Email || len(reset.Token) != 36 || reset.Token == catbird.Token {
		t.Fatalf("reset: email %q token length %d", reset.Email, len(reset.Token))
	}
	if f.canUpload(t, catbird.Game, catbird.Email, reset.Token) {
		t.Error("a reset enabled the uploader")
	}
	if st := decode[api.UploaderStatus](t, f, post(base+"/enable", token, ""), http.StatusOK); !st.Active {
		t.Errorf("enable: %+v", st)
	}
	if !f.canUpload(t, catbird.Game, catbird.Email, reset.Token) {
		t.Error("the new token does not authorize an upload")
	}
	if f.canUpload(t, catbird.Game, catbird.Email, catbird.Token) {
		t.Error("the old token still authorizes an upload")
	}
	if strings.Contains(f.logs.String(), reset.Token) {
		t.Error("the token was logged")
	}
	// The other uploader of the game is untouched.
	if !f.canUpload(t, "game01", "spoonbill@example.com", "942a563c-c911-4c84-848b-fe4e96122d8c") {
		t.Error("another uploader's token changed")
	}
}

func TestUploaderScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	other := decode[api.UploaderList](t, f, get("/api/games/game02/uploaders", f.login(t, acct02.Email, acct02.Password)), http.StatusOK)
	otherID := jsonInt(other.Uploaders[0].ID) // caracara, in game02

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
	check(get("/api/games/game02/uploaders", token))
	check(get("/api/games/nope/uploaders", token))
	check(post("/api/games/game02/uploaders", token, `{"email":"wren@example.com"}`))
	check(post("/api/games/nope/uploaders", token, `{"email":"wren@example.com"}`))
	for _, action := range []string{"disable", "enable", "reset-token"} {
		check(post("/api/games/game02/uploaders/"+otherID+"/"+action, token, "")) // someone else's game
		check(post("/api/games/game01/uploaders/"+otherID+"/"+action, token, "")) // their uploader through a managed game
		check(post("/api/games/game01/uploaders/999/"+action, token, ""))
		check(post("/api/games/game01/uploaders/abc/"+action, token, ""))
		check(post("/api/games/game01/uploaders/0/"+action, token, ""))
	}
	if !f.canUpload(t, caracara.Game, caracara.Email, caracara.Token) {
		t.Error("another game's uploader was changed")
	}
	if _, ok, _ := f.s.FindAccount(t.Context(), "wren@example.com"); ok {
		t.Error("a refused request created an account")
	}

	// A disabled manager loses the routes; an inactive game does not.
	if err := f.s.SetGameActive(t.Context(), "game01", false); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(post("/api/games/game01/uploaders", token, `{"email":"wren@example.com"}`)); w.Code != http.StatusCreated {
		t.Errorf("inactive game: %d", w.Code)
	}
	if err := f.s.SetManagerActive(t.Context(), "game01", acct01.Email, false); err != nil {
		t.Fatal(err)
	}
	check(get("/api/games/game01/uploaders", token))
	check(post("/api/games/game01/uploaders", token, `{"email":"lark@example.com"}`))
}
