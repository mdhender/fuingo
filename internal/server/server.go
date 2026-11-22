// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server assembles the HTTP handler: the browser-facing pages and
// the /api/ routes for game masters.
//
// Every route is wrapped in the standard library's cross-origin protection.
// The development and agent account helpers are registered only under the
// prefix for the configured environment.
package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Config configures the handler.
type Config struct {
	// Env is the application environment: development, test, production,
	// or agent.
	Env    string
	Store  *store.Store
	Logger *slog.Logger
	// TrustedProxies are the reverse proxies whose X-Forwarded-For header
	// names the client.
	TrustedProxies []netip.Prefix
	// LoginFailures limits failed API logins per client. If nil, the api
	// package's default limit applies.
	LoginFailures *ratelimit.Limiter
}

// HelperPrefix returns the path prefix of the account helpers in env, or ""
// if env has none.
func HelperPrefix(env string) string {
	switch env {
	case "development":
		return "/__development"
	case "agent":
		return "/__agent"
	}
	return ""
}

// New returns the handler for every route.
func New(cfg Config) (http.Handler, error) {
	clients := clientip.New(cfg.TrustedProxies)
	apiHandler, err := api.New(api.Config{
		Store:         cfg.Store,
		Logger:        cfg.Logger,
		Clients:       clients,
		LoginFailures: cfg.LoginFailures,
	})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", servePage("index.html"))
	mux.HandleFunc("GET "+HTMXPath, serveHTMX)
	mux.Handle("/api/", apiHandler)
	if prefix := HelperPrefix(cfg.Env); prefix != "" && devdata.Allowed(cfg.Env) {
		h := &helpers{store: cfg.Store, logger: cfg.Logger, api: apiHandler}
		mux.HandleFunc("POST "+prefix+"/create-api-account/{email}", h.createAPIAccount)
		mux.HandleFunc("POST "+prefix+"/log-me-in/{email}", h.logMeIn)
	}
	// A refused cross-origin request to /api/ gets the API's JSON error;
	// any other gets the standard plain-text one.
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.Forbidden(w, r)
			return
		}
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
	}))
	return protection.Handler(mux), nil
}

// helpers are the development and agent account helpers in TESTING.md.
type helpers struct {
	store  *store.Store
	logger *slog.Logger
	api    *api.Handler
}

// createAPIAccount creates an active account with a random password and no
// managed games, and returns the password. It never modifies an existing
// account. The password is never logged.
func (h *helpers) createAPIAccount(w http.ResponseWriter, r *http.Request) {
	email, err := names.Email(r.PathValue("email"))
	if err != nil {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}
	password := rand.Text()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err == nil {
		_, err = h.store.CreateAccount(r.Context(), store.NewAccount{Email: email, HashedSecret: hash})
	}
	switch {
	case errors.Is(err, store.ErrAccountExists):
		http.Error(w, "account already exists", http.StatusConflict)
	case err != nil:
		h.logger.Error("create-api-account", "email", email, "err", err)
		http.Error(w, "could not create the API account", http.StatusInternalServerError)
	default:
		h.logger.Info("create-api-account", "email", email)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, password)
	}
}

// logMeIn issues an API bearer token for an existing, active account with a
// password, as /api/login does, without checking the password. The token is
// never logged.
func (h *helpers) logMeIn(w http.ResponseWriter, r *http.Request) {
	email, err := names.Email(r.PathValue("email"))
	if err != nil {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}
	account, found, err := h.store.FindAccount(r.Context(), email)
	switch {
	case err != nil:
		h.logger.Error("log-me-in", "email", email, "err", err)
		http.Error(w, "could not log in", http.StatusInternalServerError)
		return
	case !found:
		http.Error(w, "unknown account", http.StatusNotFound)
		return
	case account.HashedSecret == nil:
		http.Error(w, "the account has no password", http.StatusForbidden)
		return
	case !account.Active:
		http.Error(w, "the account is disabled", http.StatusForbidden)
		return
	}
	resp, err := h.api.IssueToken(r, account.ID)
	if err != nil {
		h.logger.Error("log-me-in", "email", email, "err", err)
		http.Error(w, "could not log in", http.StatusInternalServerError)
		return
	}
	h.logger.Info("log-me-in", "email", email)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}
