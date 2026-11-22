// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package links_test

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/mail/mailtest"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type fixture struct {
	*links.Service
	s    *store.Store
	mail *mailtest.Recorder
	now  time.Time
	id   int64
}

const email = "owner@example.com"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f := &fixture{s: s, mail: &mailtest.Recorder{}, now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	f.Service = &links.Service{Store: s, Sender: f.mail, PublicURL: "https://fuingo.example.com", Now: func() time.Time { return f.now }}
	if f.id, err = s.CreateAccount(t.Context(), store.NewAccount{Email: email}); err != nil {
		t.Fatal(err)
	}
	return f
}

// send mails a link and returns its token.
func (f *fixture) send(t *testing.T, kind string) string {
	t.Helper()
	if err := f.Send(t.Context(), f.id, email, kind); err != nil {
		t.Fatal(err)
	}
	msgs := f.mail.Messages()
	_, token := mailtest.Link(msgs[len(msgs)-1])
	if token == "" {
		t.Fatal("the mail has no link")
	}
	return token
}

func (f *fixture) works(t *testing.T, token string) bool {
	t.Helper()
	got, found, err := f.Find(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if found && got != email {
		t.Errorf("Find = %q, want %q", got, email)
	}
	return found
}

func (f *fixture) hash(t *testing.T) []byte {
	t.Helper()
	a, _, err := f.s.FindAccount(t.Context(), email)
	if err != nil {
		t.Fatal(err)
	}
	return a.HashedSecret
}

func TestLinkExpiresAfter72Hours(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{store.LinkInvitation, store.LinkReset} {
		f := newFixture(t)
		token := f.send(t, kind)
		f.now = f.now.Add(72*time.Hour - time.Second)
		if !f.works(t, token) {
			t.Errorf("%s: refused a second before 72 hours", kind)
		}
		f.now = f.now.Add(time.Second)
		if f.works(t, token) {
			t.Errorf("%s: found at 72 hours", kind)
		}
		if _, ok, err := f.SetPassword(t.Context(), token, "a pass phrase"); err != nil || ok {
			t.Errorf("%s: SetPassword at 72 hours = %v, %v", kind, ok, err)
		}
		if f.hash(t) != nil {
			t.Errorf("%s: an expired link set a password", kind)
		}
		// A new link works.
		if fresh := f.send(t, kind); !f.works(t, fresh) {
			t.Errorf("%s: a new link after expiry does not work", kind)
		}
	}
}

func TestLinkWorksOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.send(t, store.LinkInvitation)
	got, ok, err := f.SetPassword(t.Context(), token, "first pass phrase")
	if err != nil || !ok || got != email {
		t.Fatalf("SetPassword = %q, %v, %v", got, ok, err)
	}
	if err := bcrypt.CompareHashAndPassword(f.hash(t), []byte("first pass phrase")); err != nil {
		t.Fatalf("the password was not set: %v", err)
	}
	if cost, _ := bcrypt.Cost(f.hash(t)); cost != bcrypt.MinCost {
		t.Errorf("bcrypt cost = %d, want MinCost", cost)
	}
	if f.works(t, token) {
		t.Error("a used link is still found")
	}
	if _, ok, _ := f.SetPassword(t.Context(), token, "second pass phrase"); ok {
		t.Error("a link worked twice")
	}
	if bcrypt.CompareHashAndPassword(f.hash(t), []byte("first pass phrase")) != nil {
		t.Error("the second use changed the password")
	}
	for _, bad := range []string{"", "nonsense", token + "x", strings.ToLower(token)} {
		if _, ok, err := f.SetPassword(t.Context(), bad, "a pass phrase"); err != nil || ok {
			t.Errorf("SetPassword with token %q = %v, %v", bad, ok, err)
		}
	}
}

// Of several requests with one link, one sets the password.
func TestLinkUsedConcurrently(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.send(t, store.LinkReset)
	var used atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, ok, err := f.SetPassword(t.Context(), token, "a pass phrase"); err != nil {
				t.Error(err)
			} else if ok {
				used.Add(1)
			}
		})
	}
	wg.Wait()
	if used.Load() != 1 {
		t.Errorf("the link worked %d times, want 1", used.Load())
	}
}

func TestNewLinkReplacesOlder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.send(t, store.LinkInvitation)
	second := f.send(t, store.LinkReset)
	if f.works(t, first) || !f.works(t, second) {
		t.Error("want only the newest link to work")
	}
	// A link that fails to send still replaces the older one: it may have left.
	f.mail.Fail(errors.New("down"))
	if err := f.Send(t.Context(), f.id, email, store.LinkReset); err == nil {
		t.Fatal("Send succeeded with a failing sender")
	}
	if f.works(t, second) {
		t.Error("the older link works after a newer one was made")
	}
	// Another account's links are untouched.
	f.mail.Fail(nil)
	other, err := f.s.CreateAccount(t.Context(), store.NewAccount{Email: "other@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	mine := f.send(t, store.LinkReset)
	if err := f.Send(t.Context(), other, "other@example.com", store.LinkInvitation); err != nil {
		t.Fatal(err)
	}
	if !f.works(t, mine) {
		t.Error("another account's new link canceled this one")
	}
}

func TestSendTo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if sent, err := f.SendTo(t.Context(), "nobody@example.com"); err != nil || sent {
		t.Errorf("unknown account: %v, %v", sent, err)
	}
	if sent, err := f.SendTo(t.Context(), email); err != nil || !sent {
		t.Fatalf("SendTo = %v, %v", sent, err)
	}
	// No password yet: the mail is an invitation. With one: a reset.
	_, token := mailtest.Link(f.mail.Messages()[0])
	if _, ok, _ := f.SetPassword(t.Context(), token, "a pass phrase"); !ok {
		t.Fatal("the link did not work")
	}
	if _, err := f.SendTo(t.Context(), email); err != nil {
		t.Fatal(err)
	}
	msgs := f.mail.Messages()
	if len(msgs) != 2 || msgs[0].Subject != "Your Fuingo account" || msgs[1].Subject != "Reset your Fuingo password" {
		t.Fatalf("mail = %+v", msgs)
	}
	for _, m := range msgs {
		if m.To != email || !strings.Contains(m.Text, "72 hours") || !strings.Contains(m.Text, "works once") {
			t.Errorf("message %q to %s:\n%s", m.Subject, m.To, m.Text)
		}
	}
	// A disabled account gets no mail, and its outstanding link stops working.
	_, live := mailtest.Link(msgs[1])
	if err := f.s.SetAccountActive(t.Context(), email, false); err != nil {
		t.Fatal(err)
	}
	if sent, err := f.SendTo(t.Context(), email); err != nil || sent {
		t.Errorf("disabled account: %v, %v", sent, err)
	}
	if f.works(t, live) {
		t.Error("a disabled account's link works")
	}
	if len(f.mail.Messages()) != 2 {
		t.Error("a disabled account was mailed")
	}
}

// Only a hash of the token is stored.
func TestTokenIsNotStored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.send(t, store.LinkInvitation)
	b := f.send(t, store.LinkInvitation)
	if a == b || len(a) < 20 {
		t.Errorf("tokens %q and %q", a, b)
	}
	if _, found, _ := f.s.AccountLink(t.Context(), []byte(b), f.now); found {
		t.Error("the link was found by its token rather than its hash")
	}
}

func TestCheckPassword(t *testing.T) {
	t.Parallel()
	for password, want := range map[string]error{
		"12345678":              nil,
		"correct horse battery": nil,
		"pässwörd":              nil, // 8 characters, 10 bytes
		strings.Repeat("x", 72): nil,
		strings.Repeat("é", 36): nil, // 72 bytes
		"":                      links.ErrPasswordShort,
		"1234567":               links.ErrPasswordShort,
		strings.Repeat("x", 73): links.ErrPasswordLong,
		strings.Repeat("é", 37): links.ErrPasswordLong,
		"pass\nword":            links.ErrPasswordText,
		"pass\x00word":          links.ErrPasswordText,
		"pass\tword":            links.ErrPasswordText,
		"passw\xffrd":           links.ErrPasswordText,
	} {
		if err := links.CheckPassword(password); !errors.Is(err, want) {
			t.Errorf("CheckPassword(%q) = %v, want %v", password, err, want)
		}
	}
}

func TestPublicURL(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"https://fuingo.example.com":      "https://fuingo.example.com",
		"https://fuingo.example.com/":     "https://fuingo.example.com",
		"https://htmx-app.localhost:8443": "https://htmx-app.localhost:8443",
		"http://localhost:18443":          "http://localhost:18443",
		"http://127.0.0.1:18443":          "http://127.0.0.1:18443",
	} {
		if got, err := links.PublicURL(raw); err != nil || got != want {
			t.Errorf("PublicURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"", "fuingo.example.com", "http://fuingo.example.com", "ftp://fuingo.example.com",
		"https://fuingo.example.com/app", "https://fuingo.example.com?x=1", "https://fuingo.example.com#x",
		"https://user:pass@fuingo.example.com", "https://", "http://localhost.example.com",
	} {
		if got, err := links.PublicURL(raw); !errors.Is(err, links.ErrPublicURL) {
			t.Errorf("PublicURL(%q) = %q, %v; want ErrPublicURL", raw, got, err)
		}
	}
	if links.DefaultPublicURL("development") != links.DevelopmentURL || links.DefaultPublicURL("agent") != links.DevelopmentURL ||
		links.DefaultPublicURL("production") != "" || links.DefaultPublicURL("test") != "" {
		t.Error("DefaultPublicURL: want the development origin in development and agent only")
	}
}
