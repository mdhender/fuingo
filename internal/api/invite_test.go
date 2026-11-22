// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/mail"
	"github.com/mdhender/fuingo/internal/mail/mailtest"
)

// A GM who adds an unknown email creates its account, which is mailed one
// invitation. The GM never sees the link and never sets a password.
func TestAddPlayerSendsInvitation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gm := f.login(t, acct01.Email, acct01.Password)

	w := f.serve(post(game01Players, gm, `{"email":"Wren@Example.com"}`))
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"invitation":"sent"`) {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	msgs := f.mail.Messages()
	if len(msgs) != 1 || msgs[0].To != "wren@example.com" || msgs[0].Subject != "Your Fuingo account" {
		t.Fatalf("mail = %+v, want one invitation to wren@example.com", msgs)
	}
	link, token := mailtest.Link(msgs[0])
	if !strings.HasPrefix(link, publicURL+links.PasswordPath+"?token=") {
		t.Errorf("link = %q", link)
	}
	if !strings.Contains(msgs[0].Text, "72 hours") || !strings.Contains(msgs[0].Text, publicURL+links.ResetPath) {
		t.Errorf("the invitation does not say when it expires and where to get another:\n%s", msgs[0].Text)
	}
	// Neither the response nor the log holds the token.
	if strings.Contains(w.Body.String(), token) || strings.Contains(f.logs.String(), token) {
		t.Error("the link's token is in the response or the log")
	}
	// No route gives the GM a password or a way to set one.
	for _, field := range []string{"password", "token", "link"} {
		if strings.Contains(w.Body.String(), field) {
			t.Errorf("the response mentions %q: %s", field, w.Body)
		}
	}
	if w := f.serve(post(game01Players, gm, `{"email":"lark@example.com","password":"chosen by the GM"}`)); w.Code != http.StatusBadRequest {
		t.Errorf("adding a player with a password: %d", w.Code)
	}

	// The owner uses the link, then logs in with the password they chose.
	svc := &links.Service{Store: f.s, Now: f.clock.Now}
	if email, ok, err := svc.SetPassword(t.Context(), token, "wren's own pass phrase"); err != nil || !ok || email != "wren@example.com" {
		t.Fatalf("SetPassword = %q, %v, %v", email, ok, err)
	}
	f.login(t, "wren@example.com", "wren's own pass phrase")

	// An account that exists gets no mail when it joins a game: not one with
	// a password, and not one still waiting on its invitation.
	for _, email := range []string{caracara.Email, acct02.Email, "wren@example.com"} {
		path := game01Players
		if email == "wren@example.com" {
			path = game02Players
			gm = f.login(t, acct02.Email, acct02.Password)
		}
		if got := decode[api.NewPlayer](t, f, post(path, gm, `{"email":"`+email+`"}`), http.StatusCreated); got.Invitation != api.InvitationNone {
			t.Errorf("%s: invitation %q, want none", email, got.Invitation)
		}
	}
	// A refused request sends nothing.
	gm = f.login(t, acct01.Email, acct01.Password)
	for _, r := range []*http.Request{
		post(game01Players, gm, `{"email":"wren@example.com"}`),       // already a player
		post(game02Players, gm, `{"email":"robin@example.com"}`),      // not the GM's game
		post(game01Players, "", `{"email":"robin@example.com"}`),      // no token
		post(game01Players, gm, `{"email":"not an email"}`),           // bad email
		post(game01Players, gm, `{"email":"robin@example.com"} junk`), // bad body
	} {
		if w := f.serve(r); w.Code == http.StatusCreated {
			t.Errorf("%s %s: created", r.Method, r.URL.Path)
		}
	}
	if got := f.mail.Messages(); len(got) != 1 {
		t.Errorf("%d messages sent, want only the first invitation", len(got))
	}
	if _, found, _ := f.s.FindAccount(t.Context(), "robin@example.com"); found {
		t.Error("a refused request created an account")
	}
}

// The player is added even when the invitation cannot be mailed, and the
// response says so.
func TestAddPlayerWhenMailFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gm := f.login(t, acct01.Email, acct01.Password)
	f.mail.Fail(mail.ErrMailgunRejected)
	got := decode[api.NewPlayer](t, f, post(game01Players, gm, `{"email":"wren@example.com"}`), http.StatusCreated)
	if got.Invitation != api.InvitationFailed || got.ID <= 0 {
		t.Errorf("added = %+v, want the player with a failed invitation", got)
	}
	if !strings.Contains(f.logs.String(), "send invitation") {
		t.Error("the failure was not logged")
	}
	// The owner can still get a link.
	f.mail.Fail(nil)
	svc := &links.Service{Store: f.s, Sender: f.mail, PublicURL: publicURL, Now: f.clock.Now}
	if sent, err := svc.SendTo(t.Context(), "wren@example.com"); err != nil || !sent {
		t.Fatalf("SendTo = %v, %v", sent, err)
	}
	if msgs := f.mail.Messages(); len(msgs) != 1 || msgs[0].Subject != "Your Fuingo account" {
		t.Errorf("mail = %+v, want an invitation", msgs)
	}
}

// Choosing a new password ends the account's API sessions.
func TestPasswordResetEndsSessions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	old := f.login(t, acct01.Email, acct01.Password)
	other := f.login(t, acct02.Email, acct02.Password)
	svc := &links.Service{Store: f.s, Sender: f.mail, PublicURL: publicURL, Now: f.clock.Now}
	if _, err := svc.SendTo(t.Context(), acct01.Email); err != nil {
		t.Fatal(err)
	}
	// Asking for a link changes nothing.
	if w := f.serve(get(game01Players, old)); w.Code != http.StatusOK {
		t.Fatalf("after asking for a reset: %d", w.Code)
	}
	_, token := mailtest.Link(f.mail.Messages()[0])
	f.clock.Advance(time.Hour)
	if _, ok, err := svc.SetPassword(t.Context(), token, "a new pass phrase"); err != nil || !ok {
		t.Fatalf("SetPassword = %v, %v", ok, err)
	}
	if w := f.serve(get(game01Players, old)); w.Code != http.StatusUnauthorized {
		t.Errorf("the old session after a reset: %d, want 401", w.Code)
	}
	if w := f.serve(loginRequest(acct01.Email, acct01.Password, "")); w.Code != http.StatusUnauthorized {
		t.Errorf("the old password after a reset: %d, want 401", w.Code)
	}
	f.login(t, acct01.Email, "a new pass phrase")
	// Another account's session is untouched.
	if w := f.serve(get(game02Players, other)); w.Code != http.StatusOK {
		t.Errorf("another account's session after the reset: %d", w.Code)
	}
}
