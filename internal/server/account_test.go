// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/mail/mailtest"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/server"
	"github.com/mdhender/fuingo/internal/store"
)

var acct02 = devdata.Accounts[1]

// accountFixture is a production server whose mail is recorded, whose
// background work runs at once, and whose clock the test moves.
type accountFixture struct {
	h    http.Handler
	s    *store.Store
	mail *mailtest.Recorder
	logs *bytes.Buffer
	now  time.Time
}

func newAccountFixture(t *testing.T) *accountFixture {
	t.Helper()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := devdata.Seed(t.Context(), s, "development"); err != nil {
		t.Fatal(err)
	}
	f := &accountFixture{s: s, mail: &mailtest.Recorder{}, logs: &bytes.Buffer{}, now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return f.now }
	f.h, err = server.New(server.Config{
		Env:           "production",
		Store:         s,
		Logger:        slog.New(slog.NewTextHandler(f.logs, nil)),
		Links:         &links.Service{Store: s, Sender: f.mail, PublicURL: publicOrigin, Now: clock},
		ResetRequests: ratelimit.New(server.ResetBurst, server.ResetInterval, clock),
		Background:    func(work func()) { work() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// form returns a same-origin browser POST of a form.
func form(path, remote string, values url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, publicOrigin+path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", publicOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if remote != "" {
		r.RemoteAddr = remote
	}
	return r
}

func (f *accountFixture) askReset(email, remote string) *httptest.ResponseRecorder {
	return serve(f.h, form(links.ResetPath, remote, url.Values{"email": {email}}))
}

// lastLink returns the path and token of the link in the newest mail.
func (f *accountFixture) lastLink(t *testing.T) (path, token string) {
	t.Helper()
	msgs := f.mail.Messages()
	if len(msgs) == 0 {
		t.Fatal("no mail was sent")
	}
	link, token := mailtest.Link(msgs[len(msgs)-1])
	if !strings.HasPrefix(link, publicOrigin+links.PasswordPath+"?token=") {
		t.Fatalf("link = %q", link)
	}
	return strings.TrimPrefix(link, publicOrigin), token
}

func (f *accountFixture) setPassword(token, password, confirm string) *httptest.ResponseRecorder {
	return serve(f.h, form(links.PasswordPath, "", url.Values{"token": {token}, "password": {password}, "confirm": {confirm}}))
}

func (f *accountFixture) canLogIn(email, password string) bool {
	return serve(f.h, apiLogin(email, password)).Code == http.StatusOK
}

func TestInvitationLinkSetsPassword(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	// catbird is a player with no password.
	if w := f.askReset(strings.ToUpper(catbird.Email), ""); w.Code != http.StatusOK {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	if msgs := f.mail.Messages(); len(msgs) != 1 || msgs[0].To != catbird.Email || msgs[0].Subject != "Your Fuingo account" {
		t.Fatalf("mail = %+v, want an invitation to catbird", msgs)
	}
	path, token := f.lastLink(t)

	w := serve(f.h, httptest.NewRequest(http.MethodGet, publicOrigin+path, nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, catbird.Email) || !strings.Contains(body, `name="token" value="`+token+`"`) ||
		!strings.Contains(body, `method="post"`) || !strings.Contains(body, `type="password"`) {
		t.Fatalf("form: %d %s", w.Code, body)
	}
	for header, want := range map[string]string{
		"Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "Content-Type": "text/html; charset=utf-8", "X-Content-Type-Options": "nosniff",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("form %s = %q, want %q", header, got, want)
		}
	}
	// Looking at the form uses nothing up.
	if w := serve(f.h, httptest.NewRequest(http.MethodGet, publicOrigin+path, nil)); w.Code != http.StatusOK {
		t.Errorf("second look at the form: %d", w.Code)
	}

	// Passwords that are refused leave the link usable and set nothing.
	for name, tc := range map[string]struct{ password, confirm, message string }{
		"too short":  {"short", "short", "at least 8 characters"},
		"empty":      {"", "", "at least 8 characters"},
		"too long":   {strings.Repeat("x", 73), strings.Repeat("x", 73), "at most 72 bytes"},
		"mismatch":   {"a good pass phrase", "a good pass phrasf", "not the same"},
		"no confirm": {"a good pass phrase", "", "not the same"},
		"control":    {"a good\x00pass phrase", "a good\x00pass phrase", "control characters"},
	} {
		w := f.setPassword(token, tc.password, tc.confirm)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.message) || !strings.Contains(w.Body.String(), `value="`+token+`"`) {
			t.Errorf("%s: %d, want 400 with %q and the form again\n%s", name, w.Code, tc.message, w.Body)
		}
		// The page never echoes a password.
		if tc.password != "" && strings.Contains(w.Body.String(), tc.password) {
			t.Errorf("%s: the page shows the password", name)
		}
	}
	if a, _, _ := f.s.FindAccount(t.Context(), catbird.Email); a.HashedSecret != nil {
		t.Fatal("a refused password was set")
	}

	w = f.setPassword(token, "catbird's own pass phrase", "catbird's own pass phrase")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Your password is set") || strings.Contains(w.Body.String(), token) {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if !f.canLogIn(catbird.Email, "catbird's own pass phrase") {
		t.Error("the new password does not log in")
	}

	// The link worked once.
	gone := serve(f.h, httptest.NewRequest(http.MethodGet, publicOrigin+path, nil))
	if gone.Code != http.StatusNotFound || !strings.Contains(gone.Body.String(), "no longer works") || !strings.Contains(gone.Body.String(), links.ResetPath) {
		t.Errorf("used link: %d %s", gone.Code, gone.Body)
	}
	if w := f.setPassword(token, "another pass phrase", "another pass phrase"); w.Code != http.StatusNotFound || w.Body.String() != gone.Body.String() {
		t.Errorf("second use: %d", w.Code)
	}
	if f.canLogIn(catbird.Email, "another pass phrase") || !f.canLogIn(catbird.Email, "catbird's own pass phrase") {
		t.Error("the second use changed the password")
	}

	// Nothing secret reached the log.
	logs := f.logs.String()
	for _, secret := range []string{token, "catbird's own pass phrase", "a good pass phrase", "token="} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
	if !strings.Contains(logs, "password set from a link") {
		t.Error("setting the password was not logged")
	}
}

// Every link that cannot set a password gets the same answer.
func TestLinksThatDoNotWork(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	get := func(token string) *httptest.ResponseRecorder {
		return serve(f.h, httptest.NewRequest(http.MethodGet, publicOrigin+links.PasswordPath+"?token="+url.QueryEscape(token), nil))
	}
	want := serve(f.h, httptest.NewRequest(http.MethodGet, publicOrigin+links.PasswordPath, nil)) // no token at all
	if want.Code != http.StatusNotFound || !strings.Contains(want.Body.String(), "no longer works") {
		t.Fatalf("no token: %d %s", want.Code, want.Body)
	}
	check := func(name, token string) {
		t.Helper()
		for what, w := range map[string]*httptest.ResponseRecorder{"GET": get(token), "POST": f.setPassword(token, "a good pass phrase", "a good pass phrase")} {
			if w.Code != http.StatusNotFound || w.Body.String() != want.Body.String() {
				t.Errorf("%s, %s: %d, want the 404 of a link that never existed", name, what, w.Code)
			}
		}
	}
	check("made up", "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	check("empty", "")

	// Replaced by a newer link.
	f.askReset(acct01.Email, "")
	_, first := f.lastLink(t)
	f.askReset(acct01.Email, "")
	_, second := f.lastLink(t)
	check("replaced", first)
	if get(second).Code != http.StatusOK {
		t.Fatal("the newest link does not work")
	}
	// Expired, to the second.
	f.now = f.now.Add(72*time.Hour - time.Second)
	if get(second).Code != http.StatusOK {
		t.Error("the link stopped working before 72 hours")
	}
	f.now = f.now.Add(time.Second)
	check("expired", second)

	// Of a disabled account.
	f.askReset(acct02.Email, "")
	_, disabled := f.lastLink(t)
	if err := f.s.SetAccountActive(t.Context(), acct02.Email, false); err != nil {
		t.Fatal(err)
	}
	check("disabled account", disabled)

	// None of them changed a password.
	if !f.canLogIn(acct01.Email, acct01.Password) {
		t.Error("a link that does not work changed acct01's password")
	}
}

// Asking for a link answers the same whether or not the email has an
// account.
func TestResetRequestRevealsNothing(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	page := serve(f.h, httptest.NewRequest(http.MethodGet, publicOrigin+links.ResetPath, nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="email"`) || !strings.Contains(page.Body.String(), `method="post"`) {
		t.Fatalf("reset page: %d %s", page.Code, page.Body)
	}
	if err := f.s.SetAccountActive(t.Context(), acct02.Email, false); err != nil {
		t.Fatal(err)
	}
	known := f.askReset(acct01.Email, "192.0.2.1:1000")
	if known.Code != http.StatusOK || !strings.Contains(known.Body.String(), "If that address has an account") {
		t.Fatalf("known email: %d %s", known.Code, known.Body)
	}
	if msgs := f.mail.Messages(); len(msgs) != 1 || msgs[0].To != acct01.Email || msgs[0].Subject != "Reset your Fuingo password" {
		t.Fatalf("mail = %+v, want one reset to acct01", msgs)
	}
	for name, email := range map[string]string{"unknown": "nobody@example.com", "disabled": acct02.Email} {
		w := f.askReset(email, "192.0.2.2:1000")
		if w.Code != known.Code || w.Body.String() != known.Body.String() || !equalHeaders(w.Header(), known.Header()) {
			t.Errorf("%s email: the response differs from a known email's", name)
		}
	}
	// Mail that cannot be sent changes nothing the client sees.
	f.mail.Fail(errors.New("mailgun is down"))
	if w := f.askReset(acct01.Email, "192.0.2.3:1000"); w.Code != known.Code || w.Body.String() != known.Body.String() {
		t.Errorf("mail failure: %d", w.Code)
	}
	if !strings.Contains(f.logs.String(), "send reset link") {
		t.Error("the mail failure was not logged")
	}
	f.mail.Fail(nil)
	if got := f.mail.Messages(); len(got) != 1 {
		t.Errorf("%d messages, want only acct01's", len(got))
	}
	// The password still works until the owner chooses another.
	if !f.canLogIn(acct01.Email, acct01.Password) {
		t.Error("asking for a reset changed the password")
	}
	// Something that is not an email is told so; that reveals no account.
	for _, bad := range []string{"", "not an email", "a b@example.com"} {
		if w := f.askReset(bad, "192.0.2.4:1000"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Enter an email address") {
			t.Errorf("email %q: %d", bad, w.Code)
		}
	}
	if strings.Contains(f.logs.String(), "token") {
		t.Error("the log mentions a token")
	}
}

func equalHeaders(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if strings.Join(a[k], "\n") != strings.Join(b[k], "\n") {
			return false
		}
	}
	return true
}

func TestResetRequestsAreLimitedPerClient(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	const client, other = "192.0.2.10:1000", "192.0.2.11:1000"
	// Every request counts, whether or not the email has an account.
	for i := range server.ResetBurst {
		email := "nobody@example.com"
		if i == 0 {
			email = acct01.Email
		}
		if w := f.askReset(email, client); w.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i+1, w.Code)
		}
	}
	w := f.askReset(acct01.Email, client)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" || !strings.Contains(w.Body.String(), "Too many requests") {
		t.Errorf("over the limit: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if got := f.mail.Messages(); len(got) != 1 {
		t.Errorf("%d messages, want one: a limited request sends nothing", len(got))
	}
	// Another client, even for the same email, is unaffected.
	if w := f.askReset(acct01.Email, other); w.Code != http.StatusOK {
		t.Errorf("another client: %d", w.Code)
	}
	// The form itself is not limited.
	r := httptest.NewRequest(http.MethodGet, publicOrigin+links.ResetPath, nil)
	r.RemoteAddr = client
	if w := serve(f.h, r); w.Code != http.StatusOK {
		t.Errorf("the form while limited: %d", w.Code)
	}
	// One request comes back per interval.
	f.now = f.now.Add(server.ResetInterval)
	if w := f.askReset(acct01.Email, client); w.Code != http.StatusOK {
		t.Errorf("after the interval: %d", w.Code)
	}
	if w := f.askReset(acct01.Email, client); w.Code != http.StatusTooManyRequests {
		t.Errorf("second request after the interval: %d", w.Code)
	}
}

func TestAccountPagesRefuseCrossOriginPosts(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	f.askReset(acct01.Email, "")
	_, token := f.lastLink(t)
	for _, r := range []*http.Request{
		form(links.ResetPath, "", url.Values{"email": {acct02.Email}}),
		form(links.PasswordPath, "", url.Values{"token": {token}, "password": {"a good pass phrase"}, "confirm": {"a good pass phrase"}}),
	} {
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		if w := serve(f.h, r); w.Code != http.StatusForbidden {
			t.Errorf("cross-origin POST %s: %d, want 403", r.URL.Path, w.Code)
		}
	}
	if len(f.mail.Messages()) != 1 || !f.canLogIn(acct01.Email, acct01.Password) {
		t.Error("a cross-origin request sent mail or changed a password")
	}
	// The pages answer only GET and POST.
	for _, path := range []string{links.PasswordPath, links.ResetPath} {
		if w := serve(f.h, httptest.NewRequest(http.MethodDelete, publicOrigin+path, nil)); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("DELETE %s: %d", path, w.Code)
		}
	}
	// An oversized form is refused.
	big := form(links.PasswordPath, "", url.Values{"token": {token}, "password": {strings.Repeat("x", 8<<10)}})
	if w := serve(f.h, big); w.Code != http.StatusBadRequest {
		t.Errorf("oversized form: %d", w.Code)
	}
}

// Without mail settings the pages work and nothing is sent.
func TestResetWithoutMail(t *testing.T) {
	t.Parallel()
	h, s := newServer(t, "development")
	_ = s
	if w := serve(h, form(links.ResetPath, "", url.Values{"email": {acct01.Email}})); w.Code != http.StatusOK {
		t.Errorf("reset without mail: %d", w.Code)
	}
}
