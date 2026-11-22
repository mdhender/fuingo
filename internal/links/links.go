// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package links creates the links that let an account's owner set a
// password, mails them, and redeems them.
//
// A link is mailed to the account's address as an invitation, for an
// account that has never had a password, or as a password reset. It holds a
// random token, of which only the SHA-256 is stored. It works once, expires
// after Lifetime, and stops working when a newer link is sent to the same
// account. Tokens and links are never logged.
package links

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/mail"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	// Lifetime is how long a link can set a password.
	Lifetime = 72 * time.Hour
	// PasswordPath is the page a link opens; its token is in the query.
	PasswordPath = "/account/password"
	// ResetPath is the page where an account's owner asks for a new link.
	ResetPath = "/account/reset"
	// MinPasswordLength is the fewest characters a password may hold.
	MinPasswordLength = 8
	// MaxPasswordBytes is the most bytes a password may hold: bcrypt's limit.
	MaxPasswordBytes = 72
)

const (
	ErrPasswordShort = cerrs.Error("the password must be at least 8 characters")
	ErrPasswordLong  = cerrs.Error("the password must be at most 72 bytes")
	ErrPasswordText  = cerrs.Error("the password must be text without control characters")
	ErrPublicURL     = cerrs.Error("the public URL must be an origin such as https://fuingo.example.com, with https unless the host is localhost")
)

// CheckPassword checks a password its owner chose.
func CheckPassword(password string) error {
	if !utf8.ValidString(password) {
		return ErrPasswordText
	}
	for _, r := range password {
		if r < 0x20 || r == 0x7f {
			return ErrPasswordText
		}
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrPasswordShort
	}
	if len(password) > MaxPasswordBytes {
		return ErrPasswordLong
	}
	return nil
}

// PublicURL checks the origin that links point to and returns it without a
// trailing slash. It must be https, or http for localhost and loopback
// addresses, with no path, query, or credentials.
func PublicURL(raw string) (string, error) {
	raw = strings.TrimSuffix(raw, "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", ErrPublicURL
	}
	host := u.Hostname()
	local := host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && (u.Scheme != "http" || !local) {
		return "", ErrPublicURL
	}
	return raw, nil
}

// DevelopmentURL is the origin of the shared development proxy.
const DevelopmentURL = "https://htmx-app.localhost:8443"

// DefaultPublicURL returns the public URL assumed in env when none is
// configured: DevelopmentURL in development and agent, and none elsewhere.
func DefaultPublicURL(env string) string {
	if env == "development" || env == "agent" {
		return DevelopmentURL
	}
	return ""
}

// Service creates, mails, and redeems links.
type Service struct {
	Store  *store.Store
	Sender mail.Sender
	// PublicURL is the origin the links point to, as PublicURL returns it.
	PublicURL string
	// Now returns the current time. If nil, it is time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Send records a new link for the account and mails it to the address. The
// kind is store.LinkInvitation or store.LinkReset and only chooses the
// message's wording. The account's earlier unused links stop working, even
// if the mail then fails.
func (s *Service) Send(ctx context.Context, accountID int64, email, kind string) error {
	token := rand.Text()
	now := s.now()
	if err := s.Store.CreateAccountLink(ctx, accountID, kind, hash(token), now, now.Add(Lifetime)); err != nil {
		return err
	}
	return s.Sender.Send(ctx, s.message(email, kind, token))
}

// SendTo mails a link to the account with the email: an invitation if it
// has no password yet, a reset otherwise. It reports false, and sends
// nothing, if there is no such account or it is inactive.
func (s *Service) SendTo(ctx context.Context, email string) (bool, error) {
	account, found, err := s.Store.FindAccount(ctx, email)
	if err != nil || !found || !account.Active {
		return false, err
	}
	kind := store.LinkReset
	if account.HashedSecret == nil {
		kind = store.LinkInvitation
	}
	return true, s.Send(ctx, account.ID, account.Email, kind)
}

func (s *Service) message(email, kind, token string) mail.Message {
	link := s.PublicURL + PasswordPath + "?token=" + url.QueryEscape(token)
	again := s.PublicURL + ResetPath
	if kind == store.LinkInvitation {
		return mail.Message{
			To:      email,
			Subject: "Your Fuingo account",
			Text: "An account has been created for you on Fuingo, where your play-by-email\n" +
				"games exchange orders and reports.\n\n" +
				"Choose your password here:\n\n" +
				"  " + link + "\n\n" +
				"The link works once and expires in 72 hours. After that, ask for a new\n" +
				"one at " + again + "\n\n" +
				"If you were not expecting this, you can ignore this message.\n",
		}
	}
	return mail.Message{
		To:      email,
		Subject: "Reset your Fuingo password",
		Text: "Someone asked to reset the password of your Fuingo account.\n\n" +
			"Choose a new password here:\n\n" +
			"  " + link + "\n\n" +
			"The link works once and expires in 72 hours. After that, ask for a new\n" +
			"one at " + again + "\n\n" +
			"If this was not you, ignore this message: your password has not changed.\n",
	}
}

// Find returns the email of the account that the link with the token
// belongs to. It reports false unless the link can still set a password.
func (s *Service) Find(ctx context.Context, token string) (string, bool, error) {
	la, found, err := s.Store.AccountLink(ctx, hash(token), s.now())
	return la.Email, found, err
}

// SetPassword redeems the link with the token: it sets the account's
// password, which the caller has checked with CheckPassword, and ends the
// account's sessions. It reports false, and changes nothing, unless the
// link can still set a password. The password is hashed with bcrypt at
// bcrypt.MinCost.
func (s *Service) SetPassword(ctx context.Context, token, password string) (email string, ok bool, err error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		return "", false, err
	}
	la, ok, err := s.Store.UseAccountLink(ctx, hash(token), hashed, s.now())
	return la.Email, ok, err
}
