// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/ratelimit"
)

const (
	// ResetBurst is how many reset links a client may ask for before it is
	// limited.
	ResetBurst = 5
	// ResetInterval is how often a limited client regains one request.
	ResetInterval = time.Minute
	// maxAccountForm bounds the body of an account form.
	maxAccountForm = 4 << 10
	// resetMailTimeout bounds the mailing of one reset link.
	resetMailTimeout = 30 * time.Second
)

// accountPages serves the pages where an account's owner sets a password
// from a mailed link and asks for a new link.
//
// Neither page says whether an email has an account, and no token, link, or
// password is logged.
type accountPages struct {
	links   *links.Service
	logger  *slog.Logger
	clients clientip.Resolver
	resets  *ratelimit.Limiter
	// run runs work that must not delay the response.
	run func(func())
}

// accountPage is the data of the account templates.
type accountPage struct {
	Tailwind   string
	State      string
	Action     string
	ResetPath  string
	SignInPath string
	Email      string
	Token      string
	Error      string
	MinLength  int
}

func (p *accountPages) render(w http.ResponseWriter, status int, name string, data accountPage) {
	data.Tailwind, data.ResetPath, data.MinLength = TailwindURL, links.ResetPath, links.MinPasswordLength
	data.SignInPath = SignInPath
	var buf bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		p.logger.Error("account page", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// The page's address may hold a link's token: send it to no other site.
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (p *accountPages) serverError(w http.ResponseWriter, what string, err error) {
	p.logger.Error(what, "err", err)
	http.Error(w, "the request could not be completed; try again later", http.StatusInternalServerError)
}

// badLink answers a link that cannot set a password, whatever the reason.
func (p *accountPages) badLink(w http.ResponseWriter) {
	p.render(w, http.StatusNotFound, "password.html", accountPage{State: "invalid"})
}

// showPassword serves the form a mailed link opens.
func (p *accountPages) showPassword(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	email, found, err := p.links.Find(r.Context(), token)
	if err != nil {
		p.serverError(w, "account: find link", err)
		return
	} else if !found {
		p.badLink(w)
		return
	}
	p.render(w, http.StatusOK, "password.html", accountPage{State: "form", Action: links.PasswordPath, Email: email, Token: token})
}

// setPassword redeems a link with the password its owner chose.
func (p *accountPages) setPassword(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	token, password := r.PostForm.Get("token"), r.PostForm.Get("password")
	email, found, err := p.links.Find(r.Context(), token)
	if err != nil {
		p.serverError(w, "account: find link", err)
		return
	} else if !found {
		p.badLink(w)
		return
	}
	form := accountPage{State: "form", Action: links.PasswordPath, Email: email, Token: token}
	if err := links.CheckPassword(password); err != nil {
		form.Error = "That password cannot be used: " + err.Error() + "."
		p.render(w, http.StatusBadRequest, "password.html", form)
		return
	}
	if password != r.PostForm.Get("confirm") {
		form.Error = "The two passwords are not the same."
		p.render(w, http.StatusBadRequest, "password.html", form)
		return
	}
	email, ok, err := p.links.SetPassword(r.Context(), token, password)
	if err != nil {
		p.serverError(w, "account: set password", err)
		return
	} else if !ok { // used or replaced since the check above
		p.badLink(w)
		return
	}
	p.logger.Info("account: password set from a link", "email", email)
	p.render(w, http.StatusOK, "password.html", accountPage{State: "done", Email: email})
}

func (p *accountPages) showReset(w http.ResponseWriter, r *http.Request) {
	p.render(w, http.StatusOK, "reset.html", accountPage{State: "form", Action: links.ResetPath})
}

// requestReset mails a link to the account with the given email, if there
// is one. The response is the same either way, and is not delayed by the
// mail, so it does not reveal whether the email has an account.
func (p *accountPages) requestReset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	// Every request counts against the client, whatever its email.
	client := p.clients.Addr(r)
	if ok, wait := p.resets.Take(client); !ok {
		p.logger.Warn("account: too many reset requests", "client", client)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		p.render(w, http.StatusTooManyRequests, "reset.html", accountPage{State: "limited"})
		return
	}
	email, err := names.Email(r.PostForm.Get("email"))
	if err != nil {
		p.render(w, http.StatusBadRequest, "reset.html", accountPage{State: "form", Action: links.ResetPath, Error: "Enter an email address."})
		return
	}
	p.run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), resetMailTimeout)
		defer cancel()
		sent, err := p.links.SendTo(ctx, email)
		if err != nil {
			p.logger.Error("account: send reset link", "client", client, "email", email, "err", err)
		} else if sent {
			p.logger.Info("account: sent reset link", "client", client, "email", email)
		}
	})
	p.render(w, http.StatusOK, "reset.html", accountPage{State: "sent"})
}
