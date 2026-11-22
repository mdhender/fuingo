// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	// SessionCookie holds a web session's token. The __Host- prefix makes
	// browsers accept it only as a Secure cookie for this host and every
	// path, which PROXIES.md requires whatever the backend's transport.
	SessionCookie = "__Host-fuingo_session"
	// SessionLifetime is how long a web session lasts after its last use.
	SessionLifetime = 30 * 24 * time.Hour
	// SignInPath is the page where an account signs in.
	SignInPath = "/sign-in"
	// SignOutPath ends the session of the request.
	SignOutPath = "/sign-out"
)

// sessions signs accounts in and out of the web pages.
//
// A session is a random token in a cookie, stored only as its SHA-256. No
// token or password is logged, and every failed sign-in gets the same
// response.
type sessions struct {
	db      *store.Store
	logger  *slog.Logger
	clients clientip.Resolver
	// failures limits failed sign-ins per client. It is shared with
	// /api/login.
	failures *ratelimit.Limiter
	now      func() time.Time
	// dummyHash is compared against when an account has no usable password,
	// so that every failed sign-in takes as long.
	dummyHash []byte
}

func newSessions(db *store.Store, logger *slog.Logger, clients clientip.Resolver, failures *ratelimit.Limiter, now func() time.Time) (*sessions, error) {
	dummy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), bcrypt.MinCost)
	if err != nil {
		return nil, err
	}
	return &sessions{db: db, logger: logger, clients: clients, failures: failures, now: now, dummyHash: dummy}, nil
}

// sessionPage is the data of the sign-in and home templates.
type sessionPage struct {
	HTMX, Tailwind string
	State          string
	SignInPath     string
	SignOutPath    string
	ResetPath      string
	Email          string
	Error          string
}

func (s *sessions) render(w http.ResponseWriter, status int, name string, data sessionPage) {
	data.HTMX, data.Tailwind = HTMXPath, TailwindURL
	data.SignInPath, data.SignOutPath, data.ResetPath = SignInPath, SignOutPath, links.ResetPath
	var buf bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("page", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *sessions) serverError(w http.ResponseWriter, what string, err error) {
	s.logger.Error(what, "err", err)
	http.Error(w, "the request could not be completed; try again later", http.StatusInternalServerError)
}

// setCookie gives the browser the session's token for a full lifetime.
func setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionLifetime / time.Second),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// hashedToken returns the hash of the request's session token, or nil if it
// has none.
func hashedToken(r *http.Request) []byte {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(c.Value))
	return sum[:]
}

// account returns the account signed in on the request and renews its
// session, in the database and in the browser. It reports false if the
// request has no session that still works.
func (s *sessions) account(w http.ResponseWriter, r *http.Request) (store.Account, bool, error) {
	hash := hashedToken(r)
	if hash == nil {
		return store.Account{}, false, nil
	}
	now := s.now()
	account, found, err := s.db.UseWebSession(r.Context(), hash, now, now.Add(SessionLifetime))
	if err != nil || !found {
		return store.Account{}, false, err
	}
	c, _ := r.Cookie(SessionCookie)
	setCookie(w, c.Value)
	return account, true, nil
}

// home serves the page at /: the signed-in account's, or the way to the
// sign-in page.
func (s *sessions) home(w http.ResponseWriter, r *http.Request) {
	account, ok, err := s.account(w, r)
	if err != nil {
		s.serverError(w, "home: find session", err)
		return
	} else if !ok {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, SignInPath, http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "home.html", sessionPage{Email: account.Email})
}

func (s *sessions) showSignIn(w http.ResponseWriter, r *http.Request) {
	if _, ok, err := s.account(w, r); err != nil {
		s.serverError(w, "sign-in: find session", err)
		return
	} else if ok {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "signin.html", sessionPage{State: "form"})
}

// signIn checks an email and password and starts a session.
func (s *sessions) signIn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	rawEmail, password := r.PostForm.Get("email"), r.PostForm.Get("password")
	if rawEmail == "" || password == "" {
		s.render(w, http.StatusBadRequest, "signin.html", sessionPage{State: "form", Error: "Enter your email address and password."})
		return
	}
	// A failure is reserved before the check, so concurrent guesses cannot
	// exceed the client's limit, and given back unless the sign-in fails.
	client := s.clients.Addr(r)
	if ok, wait := s.failures.Take(client); !ok {
		s.logger.Warn("sign-in: too many failures", "client", client)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		s.render(w, http.StatusTooManyRequests, "signin.html", sessionPage{State: "limited"})
		return
	}
	// An email that could not belong to an account fails like a wrong
	// password, after the same bcrypt comparison.
	email, emailErr := names.Email(rawEmail)
	account, found, err := s.db.FindAccount(r.Context(), email)
	if err != nil {
		s.failures.Return(client)
		s.serverError(w, "sign-in: find account", err)
		return
	}
	hash := s.dummyHash
	usable := emailErr == nil && found && account.Active && account.HashedSecret != nil
	if usable {
		hash = account.HashedSecret
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || !usable {
		s.logger.Info("sign-in failed", "client", client)
		s.render(w, http.StatusUnauthorized, "signin.html", sessionPage{State: "form", Error: "That email address and password do not match an account."})
		return
	}
	s.failures.Return(client)

	token := rand.Text()
	sum := sha256.Sum256([]byte(token))
	now := s.now()
	if err := s.db.CreateWebSession(r.Context(), account.ID, sum[:], now, now.Add(SessionLifetime)); err != nil {
		s.serverError(w, "sign-in: create session", err)
		return
	}
	// Signing in again replaces the browser's session rather than adding one.
	if old := hashedToken(r); old != nil {
		if err := s.db.EndWebSession(r.Context(), old); err != nil {
			s.logger.Error("sign-in: end the earlier session", "err", err)
		}
	}
	s.logger.Info("sign-in", "client", client, "email", account.Email)
	setCookie(w, token)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// signOut ends the request's session, if it has one.
func (s *sessions) signOut(w http.ResponseWriter, r *http.Request) {
	if hash := hashedToken(r); hash != nil {
		if err := s.db.EndWebSession(r.Context(), hash); err != nil {
			s.serverError(w, "sign-out", err)
			return
		}
	}
	clearCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, SignInPath, http.StatusSeeOther)
}
