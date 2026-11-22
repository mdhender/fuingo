// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package mail_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/mail"
)

// mailgun is a stand-in for the Mailgun API that records what it is sent.
type mailgun struct {
	*httptest.Server
	status   int
	path     string
	user     string
	password string
	form     url.Values
	requests int
}

func newMailgun(t *testing.T) *mailgun {
	t.Helper()
	m := &mailgun{status: http.StatusOK}
	m.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requests++
		m.path = r.URL.Path
		m.user, m.password, _ = r.BasicAuth()
		_ = r.ParseForm()
		m.form = r.PostForm
		w.WriteHeader(m.status)
		_, _ = w.Write([]byte(`{"id":"<x>","message":"Queued. Thank you."}`))
	}))
	t.Cleanup(m.Close)
	return m
}

func TestMailgunSend(t *testing.T) {
	t.Parallel()
	m := newMailgun(t)
	g := mail.NewMailgun(mail.Config{Domain: "mg.example.com", APIKey: "key-secret", From: "Fuingo <fuingo@mg.example.com>", APIBase: m.URL}, m.Client())
	msg := mail.Message{To: "player@example.com", Subject: "Your Fuingo account", Text: "line one\n\nhttps://example.com/x?token=abc&y=1\n"}
	if err := g.Send(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if m.path != "/v3/mg.example.com/messages" || m.user != "api" || m.password != "key-secret" {
		t.Errorf("request: path %q, user %q", m.path, m.user)
	}
	for field, want := range map[string]string{"from": "Fuingo <fuingo@mg.example.com>", "to": msg.To, "subject": msg.Subject, "text": msg.Text} {
		if got := m.form.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
	if len(m.form) != 4 {
		t.Errorf("form has fields %v, want only from, to, subject, text", m.form)
	}
}

func TestMailgunSandboxRedirects(t *testing.T) {
	t.Parallel()
	m := newMailgun(t)
	g := mail.NewMailgun(mail.Config{Domain: "sandbox.mailgun.org", APIKey: "k", From: "f@sandbox.mailgun.org", APIBase: m.URL, SandboxTo: "Me@Example.com"}, m.Client())
	if err := g.Send(t.Context(), mail.Message{To: "catbird@example.com", Subject: "Hello", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if m.form.Get("to") != "Me@Example.com" || m.form.Get("subject") != "[for catbird@example.com] Hello" {
		t.Errorf("redirected: to %q, subject %q", m.form.Get("to"), m.form.Get("subject"))
	}
	// Mail for the sandbox address itself is not relabeled.
	if err := g.Send(t.Context(), mail.Message{To: "me@example.com", Subject: "Hello", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if m.form.Get("to") != "me@example.com" || m.form.Get("subject") != "Hello" {
		t.Errorf("own address: to %q, subject %q", m.form.Get("to"), m.form.Get("subject"))
	}
}

func TestMailgunFailures(t *testing.T) {
	t.Parallel()
	m := newMailgun(t)
	g := mail.NewMailgun(mail.Config{Domain: "d", APIKey: "key-secret", From: "f@d", APIBase: m.URL}, m.Client())
	for _, status := range []int{400, 401, 403, 429, 500} {
		m.status = status
		err := g.Send(t.Context(), mail.Message{To: "p@example.com", Subject: "s", Text: "t"})
		if !errors.Is(err, mail.ErrMailgunRejected) {
			t.Errorf("status %d: %v, want ErrMailgunRejected", status, err)
		}
		if err != nil && strings.Contains(err.Error(), "key-secret") {
			t.Errorf("status %d: the error holds the API key", status)
		}
	}
	// Recipients and subjects that could add headers or recipients are refused unsent.
	before := m.requests
	for _, msg := range []mail.Message{
		{To: "", Subject: "s"}, {To: "a@example.com,b@example.com", Subject: "s"},
		{To: "a@example.com\nBcc: b@example.com", Subject: "s"}, {To: "a@example.com", Subject: "s\r\nBcc: b@example.com"},
	} {
		if err := g.Send(t.Context(), msg); !errors.Is(err, mail.ErrInvalidAddress) {
			t.Errorf("Send(%q, %q) = %v, want ErrInvalidAddress", msg.To, msg.Subject, err)
		}
	}
	if m.requests != before {
		t.Error("a refused message reached Mailgun")
	}
	// An unreachable server is an error that does not hold the key.
	m.Close()
	if err := g.Send(t.Context(), mail.Message{To: "p@example.com", Subject: "s", Text: "t"}); err == nil || strings.Contains(err.Error(), "key-secret") {
		t.Errorf("unreachable: %v", err)
	}
	if err := (mail.Unconfigured{}).Send(t.Context(), mail.Message{To: "p@example.com"}); !errors.Is(err, mail.ErrNotConfigured) {
		t.Errorf("Unconfigured.Send = %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	set := func(t *testing.T, domain, key, from, base, sandbox string) {
		t.Setenv("FUINGO_MAILGUN_DOMAIN", domain)
		t.Setenv("FUINGO_MAILGUN_API_KEY", key)
		t.Setenv("FUINGO_MAILGUN_FROM", from)
		t.Setenv("FUINGO_MAILGUN_API_BASE", base)
		t.Setenv("FUINGO_MAILGUN_SANDBOX_TO_ADDR", sandbox)
	}
	t.Run("unset", func(t *testing.T) {
		set(t, "", "", "", "", "")
		c, err := mail.ConfigFromEnv("production")
		if err != nil || c.Configured() {
			t.Errorf("= %+v, %v; want not configured", c.Configured(), err)
		}
		s, err := mail.SenderFromEnv("production")
		if _, ok := s.(mail.Unconfigured); err != nil || !ok {
			t.Errorf("sender = %T, %v", s, err)
		}
	})
	t.Run("set", func(t *testing.T) {
		set(t, "mg.example.com", "k", "f@mg.example.com", "", "")
		c, err := mail.ConfigFromEnv("production")
		if err != nil || !c.Configured() || c.APIBase != mail.DefaultAPIBase {
			t.Errorf("= %v, base %q, %v", c.Configured(), c.APIBase, err)
		}
		s, err := mail.SenderFromEnv("production")
		if _, ok := s.(*mail.Mailgun); err != nil || !ok {
			t.Errorf("sender = %T, %v", s, err)
		}
	})
	t.Run("EU base", func(t *testing.T) {
		set(t, "d", "k", "f@d", "https://api.eu.mailgun.net/", "")
		if c, err := mail.ConfigFromEnv("test"); err != nil || c.APIBase != "https://api.eu.mailgun.net" {
			t.Errorf("base %q, %v", c.APIBase, err)
		}
	})
	for name, tc := range map[string]struct {
		domain, key, from, base, sandbox, env string
		want                                  error
	}{
		"no key":                {"d", "", "f@d", "", "", "development", mail.ErrIncomplete},
		"only a key":            {"", "k", "", "", "", "development", mail.ErrIncomplete},
		"no from":               {"d", "k", "", "", "", "development", mail.ErrIncomplete},
		"sandbox in production": {"d", "k", "f@d", "", "me@example.com", "production", mail.ErrSandboxInProd},
		"sandbox alone in prod": {"", "", "", "", "me@example.com", "production", mail.ErrSandboxInProd},
		"http base":             {"d", "k", "f@d", "http://api.mailgun.net", "", "development", mail.ErrInvalidAPIBase},
		"base with a path":      {"d", "k", "f@d", "https://api.mailgun.net/v3", "", "development", mail.ErrInvalidAPIBase},
		"base that is no URL":   {"d", "k", "f@d", "api.mailgun.net", "", "development", mail.ErrInvalidAPIBase},
		"domain with a path":    {"d/../x", "k", "f@d", "", "", "development", mail.ErrInvalidAddress},
		"from with a newline":   {"d", "k", "f@d\nBcc: x@example.com", "", "", "development", mail.ErrInvalidAddress},
	} {
		t.Run(name, func(t *testing.T) {
			set(t, tc.domain, tc.key, tc.from, tc.base, tc.sandbox)
			if _, err := mail.ConfigFromEnv(tc.env); !errors.Is(err, tc.want) {
				t.Errorf("= %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("sandbox outside production", func(t *testing.T) {
		set(t, "d", "k", "f@d", "", "me@example.com")
		for _, env := range []string{"development", "agent", "test"} {
			if c, err := mail.ConfigFromEnv(env); err != nil || c.SandboxTo != "me@example.com" {
				t.Errorf("%s: %q, %v", env, c.SandboxTo, err)
			}
		}
	})
}
