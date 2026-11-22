// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/server"
)

// get requests a page as a browser holding the cookie, if any.
func (f *accountFixture) get(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, publicOrigin+path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return serve(f.h, r)
}

func (f *accountFixture) postSignIn(email, password, remote string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := form(server.SignInPath, remote, url.Values{"email": {email}, "password": {password}})
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return serve(f.h, r)
}

// sessionCookie returns the session cookie the response sets, or nil.
func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == server.SessionCookie {
			return c
		}
	}
	return nil
}

// signIn signs the account in and returns its session cookie.
func (f *accountFixture) signIn(t *testing.T, email, password, remote string) *http.Cookie {
	t.Helper()
	w := f.postSignIn(email, password, remote, nil)
	c := sessionCookie(w)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" || c == nil || c.Value == "" {
		t.Fatalf("sign in %s: %d, Location %q, cookie %v", email, w.Code, w.Header().Get("Location"), c)
	}
	return c
}

// signedIn reports whether the cookie's session reaches the home page.
func (f *accountFixture) signedIn(t *testing.T, cookie *http.Cookie) bool {
	t.Helper()
	w := f.get("/", cookie)
	switch {
	case w.Code == http.StatusOK:
		return true
	case w.Code == http.StatusSeeOther && w.Header().Get("Location") == server.SignInPath:
		return false
	}
	t.Fatalf("GET /: %d, Location %q", w.Code, w.Header().Get("Location"))
	return false
}

func TestSignIn(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	// Nothing is open to an anonymous client but signing in.
	if f.signedIn(t, nil) {
		t.Fatal("the home page is open without a session")
	}
	page := f.get(server.SignInPath, nil)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, `<form method="post" action="`+server.SignInPath+`"`) ||
		!strings.Contains(body, `name="email"`) || !strings.Contains(body, `type="password"`) || !strings.Contains(body, `href="`+links.ResetPath+`"`) {
		t.Fatalf("sign-in page: %d %s", page.Code, body)
	}
	if page.Header().Get("Cache-Control") != "no-store" || sessionCookie(page) != nil {
		t.Errorf("sign-in page: Cache-Control %q, cookie %v", page.Header().Get("Cache-Control"), sessionCookie(page))
	}

	// The email is matched in lower case, as everywhere.
	c := f.signIn(t, strings.ToUpper(acct01.Email), acct01.Password, "")
	if c.Name != "__Host-fuingo_session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 30*24*60*60 {
		t.Errorf("cookie = %+v, want Secure, HttpOnly, SameSite=Lax, Path=/, no Domain, 30 days", c)
	}
	if len(c.Value) < 20 {
		t.Errorf("token %q is too short to be random", c.Value)
	}
	home := f.get("/", c)
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), acct01.Email) || home.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("home: %d %s", home.Code, home.Body)
	}
	// A signed-in browser has no use for the sign-in page.
	if w := f.get(server.SignInPath, c); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("sign-in page while signed in: %d", w.Code)
	}
	// Each sign-in gets its own token, and a made-up one is no session.
	other := f.signIn(t, acct01.Email, acct01.Password, "")
	if other.Value == c.Value {
		t.Error("two sign-ins share a token")
	}
	for _, bad := range []string{"", "nonsense", c.Value + "x", strings.ToLower(c.Value)} {
		if f.signedIn(t, &http.Cookie{Name: server.SessionCookie, Value: bad}) {
			t.Errorf("token %q is signed in", bad)
		}
	}
	// A cookie under another name is not a session.
	if f.signedIn(t, &http.Cookie{Name: "fuingo_session", Value: c.Value}) {
		t.Error("a cookie without the __Host- prefix is signed in")
	}
	// The web session is not an API token, nor the reverse.
	r := apiList(c.Value)
	if w := serve(f.h, r); w.Code != http.StatusUnauthorized {
		t.Errorf("the session token as an API token: %d", w.Code)
	}
	apiToken := tokenFrom(t, serve(f.h, apiLogin(acct01.Email, acct01.Password)))
	if f.signedIn(t, &http.Cookie{Name: server.SessionCookie, Value: apiToken}) {
		t.Error("an API token is a web session")
	}
	// Neither the token nor the password is logged.
	logs := f.logs.String()
	for _, secret := range []string{c.Value, other.Value, acct01.Password} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
	if !strings.Contains(logs, "sign-in") || !strings.Contains(logs, acct01.Email) {
		t.Error("the sign-in was not logged")
	}
}

// Every failed sign-in gets the same response, so none reveals whether an
// email has an account.
func TestSignInFailuresAreIdentical(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	if err := f.s.SetAccountActive(t.Context(), acct02.Email, false); err != nil {
		t.Fatal(err)
	}
	want := f.postSignIn(acct01.Email, "not the password", "192.0.2.1:1000", nil)
	if want.Code != http.StatusUnauthorized || !strings.Contains(want.Body.String(), "do not match an account") || sessionCookie(want) != nil {
		t.Fatalf("wrong password: %d %s", want.Code, want.Body)
	}
	if strings.Contains(want.Body.String(), "not the password") || strings.Contains(want.Body.String(), acct01.Email) {
		t.Error("the page echoes the email or the password")
	}
	for name, tc := range map[string]struct{ email, password string }{
		"unknown email":      {"nobody@example.com", "any password"},
		"no password yet":    {catbird.Email, "any password"},
		"disabled account":   {acct02.Email, acct02.Password},
		"not an email":       {"not an email", "any password"},
		"password too long":  {acct01.Email, strings.Repeat("x", 100)},
		"another's password": {acct01.Email, acct02.Password},
	} {
		// A new address each time, so the failure limit stays out of it.
		w := f.postSignIn(tc.email, tc.password, "192.0.2."+string(rune('2'+len(name)%7))+":1000", nil)
		if w.Code != want.Code || w.Body.String() != want.Body.String() || !equalHeaders(w.Header(), want.Header()) {
			t.Errorf("%s: %d, the response differs from a wrong password's", name, w.Code)
		}
	}
	// A form without both fields is not a guess.
	for _, v := range []url.Values{{"email": {acct01.Email}}, {"password": {acct01.Password}}, {}} {
		if w := serve(f.h, form(server.SignInPath, "192.0.2.9:1000", v)); w.Code != http.StatusBadRequest || sessionCookie(w) != nil {
			t.Errorf("form %v: %d", v, w.Code)
		}
	}
	if strings.Contains(f.logs.String(), "not the password") || strings.Contains(f.logs.String(), "nobody@example.com") {
		t.Error("a failed sign-in logged its password or email")
	}
}

func TestSignInFailureLimit(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	const client, other = "192.0.2.10:1000", "192.0.2.11:1000"
	// Successes do not count.
	for range api.FailureBurst + 2 {
		f.signIn(t, acct01.Email, acct01.Password, client)
	}
	// Malformed forms do not count either.
	for range api.FailureBurst + 2 {
		serve(f.h, form(server.SignInPath, client, url.Values{"email": {acct01.Email}}))
	}
	for i := range api.FailureBurst {
		if w := f.postSignIn(acct01.Email, "wrong", client, nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i+1, w.Code)
		}
	}
	// Over the limit, even the right password is refused.
	w := f.postSignIn(acct01.Email, acct01.Password, client, nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" || sessionCookie(w) != nil || !strings.Contains(w.Body.String(), "Too many failed sign-ins") {
		t.Errorf("over the limit: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	// The limit is the client's, on the web and in /api/ together.
	r := apiLogin(acct01.Email, acct01.Password)
	r.RemoteAddr = client
	if w := serve(f.h, r); w.Code != http.StatusTooManyRequests {
		t.Errorf("/api/login from the limited client: %d, want 429", w.Code)
	}
	// Another client is unaffected, even for the same email.
	f.signIn(t, acct01.Email, acct01.Password, other)
	// The page itself still loads.
	req := httptest.NewRequest(http.MethodGet, publicOrigin+server.SignInPath, nil)
	req.RemoteAddr = client
	if w := serve(f.h, req); w.Code != http.StatusOK {
		t.Errorf("the sign-in page while limited: %d", w.Code)
	}
	// One attempt comes back per interval; a success gives it back again.
	f.now = f.now.Add(api.FailureInterval)
	f.signIn(t, acct01.Email, acct01.Password, client)
	if w := f.postSignIn(acct01.Email, "wrong", client, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("after the interval: %d", w.Code)
	}
	if w := f.postSignIn(acct01.Email, acct01.Password, client, nil); w.Code != http.StatusTooManyRequests {
		t.Errorf("second attempt after the interval: %d", w.Code)
	}
}

// Concurrent guesses from one client cannot exceed its limit.
func TestConcurrentSignInFailures(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	var checked atomic.Int32
	var wg sync.WaitGroup
	for range 4 * api.FailureBurst {
		wg.Go(func() {
			if w := f.postSignIn(acct01.Email, "wrong", "192.0.2.20:1000", nil); w.Code == http.StatusUnauthorized {
				checked.Add(1)
			}
		})
	}
	wg.Wait()
	if got := checked.Load(); got != api.FailureBurst {
		t.Errorf("%d guesses were checked, want %d", got, api.FailureBurst)
	}
}

// A session lasts 30 days from its last use.
func TestSessionIsRenewedByUse(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	used := f.signIn(t, acct01.Email, acct01.Password, "")
	idle := f.signIn(t, acct01.Email, acct01.Password, "")

	f.now = f.now.Add(server.SessionLifetime - time.Second)
	w := f.get("/", used)
	if w.Code != http.StatusOK {
		t.Fatalf("a second before 30 days: %d", w.Code)
	}
	// Using it renews the browser's cookie too.
	if c := sessionCookie(w); c == nil || c.Value != used.Value || c.MaxAge != int(server.SessionLifetime/time.Second) || !c.Secure || !c.HttpOnly {
		t.Errorf("renewed cookie = %+v", c)
	}
	f.now = f.now.Add(time.Second)
	if f.signedIn(t, idle) {
		t.Error("a session unused for 30 days still works")
	}
	if !f.signedIn(t, used) {
		t.Error("a session used a second before its 30 days were up was not renewed")
	}
	// It now lasts 30 days from that use, and no longer.
	f.now = f.now.Add(server.SessionLifetime - time.Second)
	if !f.signedIn(t, used) {
		t.Error("the renewed session ended early")
	}
	f.now = f.now.Add(server.SessionLifetime)
	if f.signedIn(t, used) {
		t.Error("the session works 30 days after its last use")
	}
	// An expired session's response sets no cookie, and nothing revives it.
	if w := f.get("/", used); sessionCookie(w) != nil {
		t.Error("an expired session was sent a cookie")
	}
}

func TestSignOut(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	here := f.signIn(t, acct01.Email, acct01.Password, "")
	elsewhere := f.signIn(t, acct01.Email, acct01.Password, "")
	someone := f.signIn(t, acct02.Email, acct02.Password, "")

	// Signing out is a POST: a link cannot do it.
	if w := f.get(server.SignOutPath, here); w.Code != http.StatusMethodNotAllowed || !f.signedIn(t, here) {
		t.Errorf("GET sign-out: %d", w.Code)
	}
	r := form(server.SignOutPath, "", nil)
	r.AddCookie(here)
	w := serve(f.h, r)
	c := sessionCookie(w)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != server.SignInPath || c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("sign out: %d, Location %q, cookie %+v", w.Code, w.Header().Get("Location"), c)
	}
	// The server ended it: the old cookie is no session, even if kept.
	if f.signedIn(t, here) {
		t.Error("the session works after signing out")
	}
	// Only that session ended.
	if !f.signedIn(t, elsewhere) || !f.signedIn(t, someone) {
		t.Error("signing out ended another session")
	}
	// Signing out twice, or without a session, is harmless.
	for _, cookie := range []*http.Cookie{here, nil} {
		r := form(server.SignOutPath, "", nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if w := serve(f.h, r); w.Code != http.StatusSeeOther {
			t.Errorf("sign out again: %d", w.Code)
		}
	}
	// Signing in again in a browser that holds a session replaces it.
	w = f.postSignIn(acct01.Email, acct01.Password, "", elsewhere)
	if next := sessionCookie(w); next == nil || !f.signedIn(t, next) || f.signedIn(t, elsewhere) {
		t.Error("signing in again did not replace the browser's session")
	}
	// A failed sign-in leaves the browser's session alone.
	if w := f.postSignIn(acct02.Email, "wrong", "", someone); w.Code != http.StatusUnauthorized || !f.signedIn(t, someone) {
		t.Error("a failed sign-in ended the browser's session")
	}
}

// Choosing a new password ends every session of the account.
func TestPasswordResetEndsWebSessions(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	one := f.signIn(t, acct01.Email, acct01.Password, "")
	two := f.signIn(t, acct01.Email, acct01.Password, "")
	someone := f.signIn(t, acct02.Email, acct02.Password, "")
	apiToken := tokenFrom(t, serve(f.h, apiLogin(acct01.Email, acct01.Password)))

	f.askReset(acct01.Email, "")
	_, token := f.lastLink(t)
	// Asking for the link ends nothing.
	if !f.signedIn(t, one) || !f.signedIn(t, two) {
		t.Fatal("asking for a reset ended a session")
	}
	w := f.setPassword(token, "a new pass phrase", "a new pass phrase")
	if w.Code != http.StatusOK || sessionCookie(w) != nil {
		t.Fatalf("set password: %d, cookie %v: the link must not sign anyone in", w.Code, sessionCookie(w))
	}
	if f.signedIn(t, one) || f.signedIn(t, two) {
		t.Error("a web session survived the password reset")
	}
	if w := serve(f.h, apiList(apiToken)); w.Code != http.StatusUnauthorized {
		t.Errorf("the API token after the reset: %d", w.Code)
	}
	if !f.signedIn(t, someone) {
		t.Error("the reset ended another account's session")
	}
	if w := f.postSignIn(acct01.Email, acct01.Password, "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("the old password: %d", w.Code)
	}
	f.signIn(t, acct01.Email, "a new pass phrase", "")
}

// An inactive account can do nothing; its session works again if the
// account is enabled before the session expires.
func TestInactiveAccountSession(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	c := f.signIn(t, acct01.Email, acct01.Password, "")
	if err := f.s.SetAccountActive(t.Context(), acct01.Email, false); err != nil {
		t.Fatal(err)
	}
	if f.signedIn(t, c) {
		t.Error("a disabled account's session works")
	}
	// Being refused does not renew it.
	f.now = f.now.Add(server.SessionLifetime - time.Hour)
	if f.signedIn(t, c) {
		t.Error("a disabled account's session works")
	}
	if err := f.s.SetAccountActive(t.Context(), acct01.Email, true); err != nil {
		t.Fatal(err)
	}
	if !f.signedIn(t, c) {
		t.Error("the session does not work after the account was enabled")
	}
	if err := f.s.SetAccountActive(t.Context(), acct01.Email, false); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(server.SessionLifetime + time.Hour)
	if err := f.s.SetAccountActive(t.Context(), acct01.Email, true); err != nil {
		t.Fatal(err)
	}
	if f.signedIn(t, c) {
		t.Error("an expired session works after the account was enabled")
	}
}

func TestSessionRoutesRefuseCrossOriginPosts(t *testing.T) {
	t.Parallel()
	f := newAccountFixture(t)
	c := f.signIn(t, acct01.Email, acct01.Password, "")
	in := form(server.SignInPath, "", url.Values{"email": {acct02.Email}, "password": {acct02.Password}})
	out := form(server.SignOutPath, "", nil)
	out.AddCookie(c)
	for _, r := range []*http.Request{in, out} {
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		if w := serve(f.h, r); w.Code != http.StatusForbidden || sessionCookie(w) != nil {
			t.Errorf("cross-origin POST %s: %d, want 403", r.URL.Path, w.Code)
		}
	}
	if !f.signedIn(t, c) {
		t.Error("a cross-origin request signed the browser out")
	}
	// An oversized form is refused.
	big := form(server.SignInPath, "", url.Values{"email": {acct01.Email}, "password": {strings.Repeat("x", 8<<10)}})
	if w := serve(f.h, big); w.Code != http.StatusBadRequest {
		t.Errorf("oversized form: %d", w.Code)
	}
}
