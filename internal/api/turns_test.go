// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	game01Turns = game01Path + "/turns"
	game02Turns = game02Path + "/turns"
)

// turns lists the game's turns as the account with the token.
func (f *fixture) turns(t *testing.T, token, path string) []api.Turn {
	t.Helper()
	return decode[api.TurnList](t, f, get(path, token), http.StatusOK).Turns
}

func TestCreateTurn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)

	// A game starts with no turns; the list is [], not null.
	if w := f.serve(get(game01Turns, token)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"turns":[]`) {
		t.Errorf("no turns: %d %s", w.Code, w.Body)
	}
	// The first turn must be 0.
	if w := f.serve(post(game01Turns, token, `{"turn":1}`)); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "which is 0") {
		t.Errorf("turn 1 first: %d %s", w.Code, w.Body)
	}
	created := f.clock.Now()
	for n, body := range []string{`{"turn":0}`, " {\"turn\": 1}\r\n", `{"turn":2}`} {
		st := decode[api.TurnStatus](t, f, post(game01Turns, token, body), http.StatusCreated)
		if st.System != "system01" || st.Game != "game01" || st.Turn.Turn != int64(n) || st.Closed || st.ClosedAt != nil || st.CreatedAt != created.Format(api.TimeLayout) {
			t.Errorf("turn %d: %+v", n, st)
		}
	}
	// A retry, a gap, and an earlier number conflict, and name the next turn.
	for _, body := range []string{`{"turn":2}`, `{"turn":4}`, `{"turn":0}`} {
		if w := f.serve(post(game01Turns, token, body)); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "which is 3") {
			t.Errorf("%s: %d %s, want 409 naming turn 3", body, w.Code, w.Body)
		}
	}
	for name, tc := range map[string]struct {
		token, body string
		status      int
	}{
		"missing":         {token, `{}`, http.StatusBadRequest},
		"null":            {token, `{"turn":null}`, http.StatusBadRequest},
		"negative":        {token, `{"turn":-1}`, http.StatusBadRequest},
		"fraction":        {token, `{"turn":3.5}`, http.StatusBadRequest},
		"string":          {token, `{"turn":"3"}`, http.StatusBadRequest},
		"too large":       {token, `{"turn":99999999999999999999}`, http.StatusBadRequest},
		"a name":          {token, `{"turn":3,"name":"Spring"}`, http.StatusBadRequest},
		"closed on entry": {token, `{"turn":3,"closed":true}`, http.StatusBadRequest},
		"trailing data":   {token, `{"turn":3} junk`, http.StatusBadRequest},
		"not JSON":        {token, `turn=3`, http.StatusBadRequest},
		"no token":        {"", `{"turn":3}`, http.StatusUnauthorized},
	} {
		if w := f.serve(post(game01Turns, tc.token, tc.body)); w.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, tc.status)
		}
	}
	r := post(game01Turns, token, `{"turn":3}`)
	r.Header.Set("Content-Type", "text/plain")
	if w := f.serve(r); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", w.Code)
	}
	// Refused requests created nothing.
	turns := f.turns(t, token, game01Turns)
	if len(turns) != 3 {
		t.Fatalf("turns = %+v, want three", turns)
	}
	for n, turn := range turns {
		if turn.Turn != int64(n) || turn.Closed {
			t.Errorf("turn %d = %+v", n, turn)
		}
	}
}

func TestCloseAndReopenTurn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	for _, body := range []string{`{"turn":0}`, `{"turn":1}`, `{"turn":2}`} {
		decode[api.TurnStatus](t, f, post(game01Turns, token, body), http.StatusCreated)
	}
	f.clock.Advance(time.Hour)
	closedAt := f.clock.Now().Format(api.TimeLayout)
	st := decode[api.TurnStatus](t, f, post(game01Turns+"/1/close", token, ""), http.StatusOK)
	if st.System != "system01" || st.Game != "game01" || st.Turn.Turn != 1 || !st.Closed || st.ClosedAt == nil || *st.ClosedAt != closedAt {
		t.Errorf("close: %+v", st)
	}
	// Closing again succeeds and keeps the time it first closed.
	f.clock.Advance(time.Hour)
	if st := decode[api.TurnStatus](t, f, post(game01Turns+"/1/close", token, ""), http.StatusOK); !st.Closed || *st.ClosedAt != closedAt {
		t.Errorf("second close: %+v", st)
	}
	// Any number of turns may be open at once: closing one leaves the rest.
	turns := f.turns(t, token, game01Turns)
	if turns[0].Closed || !turns[1].Closed || turns[2].Closed {
		t.Errorf("turns = %+v, want only turn 1 closed", turns)
	}
	// A new turn can follow a closed one, and an open one.
	decode[api.TurnStatus](t, f, post(game01Turns, token, `{"turn":3}`), http.StatusCreated)

	for range 2 { // reopening twice succeeds
		if st := decode[api.TurnStatus](t, f, post(game01Turns+"/1/open", token, ""), http.StatusOK); st.Closed || st.ClosedAt != nil {
			t.Errorf("reopen: %+v", st)
		}
	}
	// A reopened turn closes again, at the new time.
	st = decode[api.TurnStatus](t, f, post(game01Turns+"/1/close", token, ""), http.StatusOK)
	if !st.Closed || *st.ClosedAt != f.clock.Now().Format(api.TimeLayout) {
		t.Errorf("close after reopening: %+v", st)
	}
	// Turn 0, the setup, closes like any other.
	if st := decode[api.TurnStatus](t, f, post(game01Turns+"/0/close", token, ""), http.StatusOK); !st.Closed {
		t.Errorf("close turn 0: %+v", st)
	}
}

func TestTurnScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.login(t, acct01.Email, acct01.Password)
	other := f.login(t, acct02.Email, acct02.Password)
	decode[api.TurnStatus](t, f, post(game01Turns, token, `{"turn":0}`), http.StatusCreated)
	for _, body := range []string{`{"turn":0}`, `{"turn":1}`} {
		decode[api.TurnStatus](t, f, post(game02Turns, other, body), http.StatusCreated)
	}

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
	const unknown = "/api/systems/system01/games/nope/turns"
	check(get(game02Turns, token))
	check(get(unknown, token))
	check(post(game02Turns, token, `{"turn":2}`))
	check(post(unknown, token, `{"turn":0}`))
	for _, action := range []string{"close", "open"} {
		check(post(game02Turns+"/0/"+action, token, "")) // someone else's game
		check(post(game01Turns+"/1/"+action, token, "")) // exists only in the other game
		check(post(game01Turns+"/9/"+action, token, ""))
		check(post(game01Turns+"/-1/"+action, token, ""))
		check(post(game01Turns+"/abc/"+action, token, ""))
		check(post(game01Turns+"/00/"+action, token, "")) // not how turn 0 is written
		check(post(game01Turns+"/+0/"+action, token, ""))
		check(post(game01Turns+"/"+strings.Repeat("9", 30)+"/"+action, token, ""))
	}
	// Nothing in the other game changed.
	turns := f.turns(t, other, game02Turns)
	if len(turns) != 2 || turns[0].Closed || turns[1].Closed {
		t.Errorf("game02's turns = %+v, want two open turns", turns)
	}
	if turns := f.turns(t, token, game01Turns); len(turns) != 1 || turns[0].Closed {
		t.Errorf("game01's turns = %+v, want one open turn", turns)
	}

	// A disabled manager loses the routes; an inactive game does not.
	if err := f.s.SetGameActive(t.Context(), game01, false); err != nil {
		t.Fatal(err)
	}
	decode[api.TurnStatus](t, f, post(game01Turns, token, `{"turn":1}`), http.StatusCreated)
	decode[api.TurnStatus](t, f, post(game01Turns+"/1/close", token, ""), http.StatusOK)
	if err := f.s.SetManagerActive(t.Context(), store.Member{Game: game01, Email: acct01.Email}, false); err != nil {
		t.Fatal(err)
	}
	check(get(game01Turns, token))
	check(post(game01Turns, token, `{"turn":2}`))
	check(post(game01Turns+"/1/open", token, ""))
}

// A player's account, even with a password, cannot reach its game's turn
// routes: they are for the game's GMs.
func TestPlayersCannotManageTurns(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gm := f.login(t, acct01.Email, acct01.Password)
	decode[api.TurnStatus](t, f, post(game01Turns, gm, `{"turn":0}`), http.StatusCreated)
	// acct02 manages game02 and plays in game01.
	if _, err := f.s.AddPlayer(t.Context(), store.Member{Game: game01, Email: acct02.Email}); err != nil {
		t.Fatal(err)
	}
	player := f.login(t, acct02.Email, acct02.Password)
	for _, r := range []*http.Request{
		get(game01Turns, player),
		post(game01Turns, player, `{"turn":1}`),
		post(game01Turns+"/0/close", player, ""),
	} {
		if w := f.serve(r); w.Code != http.StatusNotFound {
			t.Errorf("%s %s as a player: %d, want 404", r.Method, r.URL.Path, w.Code)
		}
	}
	if turns := f.turns(t, gm, game01Turns); len(turns) != 1 || turns[0].Closed {
		t.Errorf("turns = %+v, want one open turn", turns)
	}
}
