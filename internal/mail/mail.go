// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mail sends Fuingo's account mail through Mailgun.
//
// The Mailgun settings are secrets and come only from the environment:
//
//	FUINGO_MAILGUN_DOMAIN           the sending domain
//	FUINGO_MAILGUN_API_KEY          the API key
//	FUINGO_MAILGUN_FROM             the From address
//	FUINGO_MAILGUN_API_BASE         the API's origin (default https://api.mailgun.net;
//	                                https://api.eu.mailgun.net for EU accounts)
//	FUINGO_MAILGUN_SANDBOX_TO_ADDR  if set, every message is delivered to this
//	                                address instead of its recipient; refused in
//	                                production
//
// Nothing in this package logs a message, its recipient's link, or the key.
package mail

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
)

const (
	ErrNotConfigured   = cerrs.Error("mail is not configured: set FUINGO_MAILGUN_DOMAIN, FUINGO_MAILGUN_API_KEY, and FUINGO_MAILGUN_FROM")
	ErrIncomplete      = cerrs.Error("FUINGO_MAILGUN_DOMAIN, FUINGO_MAILGUN_API_KEY, and FUINGO_MAILGUN_FROM must be set together")
	ErrSandboxInProd   = cerrs.Error("FUINGO_MAILGUN_SANDBOX_TO_ADDR must not be set in production")
	ErrInvalidAPIBase  = cerrs.Error("FUINGO_MAILGUN_API_BASE must be an https origin, such as https://api.mailgun.net")
	ErrInvalidAddress  = cerrs.Error("a mail address or domain holds a character that is not allowed")
	ErrMailgunRejected = cerrs.Error("Mailgun did not accept the message")
)

// DefaultAPIBase is Mailgun's API origin for accounts in the US region.
const DefaultAPIBase = "https://api.mailgun.net"

// sendTimeout bounds one request to Mailgun.
const sendTimeout = 15 * time.Second

// Message is a plain-text message to one recipient.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Sender sends a message. An error means it was not accepted for delivery;
// what happens after that, such as a bounce, is not reported.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Unconfigured is the Sender of a program with no Mailgun settings. Every
// Send fails with ErrNotConfigured.
type Unconfigured struct{}

func (Unconfigured) Send(context.Context, Message) error { return ErrNotConfigured }

// Config holds the Mailgun settings.
type Config struct {
	Domain  string
	APIKey  string
	From    string
	APIBase string
	// SandboxTo, if set, receives every message in place of its recipient.
	SandboxTo string
}

// Configured reports whether the settings name a Mailgun account.
func (c Config) Configured() bool {
	return c.Domain != "" && c.APIKey != "" && c.From != ""
}

// ConfigFromEnv reads the Mailgun settings from the process environment for
// the application environment env. With none of the three required settings
// it returns a Config that is not Configured; with only some, ErrIncomplete.
func ConfigFromEnv(env string) (Config, error) {
	c := Config{
		Domain:    os.Getenv("FUINGO_MAILGUN_DOMAIN"),
		APIKey:    os.Getenv("FUINGO_MAILGUN_API_KEY"),
		From:      os.Getenv("FUINGO_MAILGUN_FROM"),
		APIBase:   os.Getenv("FUINGO_MAILGUN_API_BASE"),
		SandboxTo: os.Getenv("FUINGO_MAILGUN_SANDBOX_TO_ADDR"),
	}
	if c.SandboxTo != "" && env == "production" {
		return Config{}, ErrSandboxInProd
	}
	if !c.Configured() {
		if c.Domain != "" || c.APIKey != "" || c.From != "" {
			return Config{}, ErrIncomplete
		}
		return Config{}, nil
	}
	if c.APIBase == "" {
		c.APIBase = DefaultAPIBase
	}
	c.APIBase = strings.TrimSuffix(c.APIBase, "/")
	u, err := url.Parse(c.APIBase)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return Config{}, ErrInvalidAPIBase
	}
	for _, v := range []string{c.Domain, c.From, c.SandboxTo} {
		if strings.ContainsAny(v, "\r\n") {
			return Config{}, ErrInvalidAddress
		}
	}
	if strings.ContainsAny(c.Domain, "/?#% ") {
		return Config{}, ErrInvalidAddress
	}
	return c, nil
}

// SenderFromEnv returns the Sender for the settings in the process
// environment: Mailgun if they are set, Unconfigured if not.
func SenderFromEnv(env string) (Sender, error) {
	c, err := ConfigFromEnv(env)
	if err != nil {
		return nil, err
	}
	if !c.Configured() {
		return Unconfigured{}, nil
	}
	return NewMailgun(c, nil), nil
}

// Mailgun sends messages through the Mailgun HTTP API.
type Mailgun struct {
	cfg    Config
	client *http.Client
}

// NewMailgun returns a Sender for the account in cfg. If client is nil, one
// with a timeout is used.
func NewMailgun(cfg Config, client *http.Client) *Mailgun {
	if client == nil {
		client = &http.Client{Timeout: sendTimeout}
	}
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	return &Mailgun{cfg: cfg, client: client}
}

// Send asks Mailgun to deliver the message. With a sandbox address
// configured, the message goes there instead, and its subject names the
// recipient it was meant for.
func (g *Mailgun) Send(ctx context.Context, m Message) error {
	if strings.ContainsAny(m.To, "\r\n,") || strings.ContainsAny(m.Subject, "\r\n") || m.To == "" {
		return ErrInvalidAddress
	}
	to, subject := m.To, m.Subject
	if g.cfg.SandboxTo != "" && !strings.EqualFold(to, g.cfg.SandboxTo) {
		to, subject = g.cfg.SandboxTo, "[for "+m.To+"] "+m.Subject
	}
	form := url.Values{"from": {g.cfg.From}, "to": {to}, "subject": {subject}, "text": {m.Text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.APIBase+"/v3/"+url.PathEscape(g.cfg.Domain)+"/messages", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth("api", g.cfg.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.client.Do(req)
	if err != nil {
		// A *url.Error names the request's URL, which holds no secret.
		return fmt.Errorf("mailgun: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%w: status %d", ErrMailgunRejected, resp.StatusCode)
	}
	return nil
}
