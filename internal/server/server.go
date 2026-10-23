// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server assembles the browser-facing HTTP handler.
//
// Every route is wrapped in the standard library's cross-origin protection.
// The development and agent account helpers are registered only under the
// prefix for the configured environment.
package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/mdhender/fuingo/internal/upload"
)

// Config configures the handler.
type Config struct {
	// Env is the application environment: development, test, production,
	// or agent.
	Env    string
	Store  *store.Store
	Pepper string
	Logger *slog.Logger
	// TrustedProxies are the reverse proxies whose X-Forwarded-For header
	// names the client.
	TrustedProxies []netip.Prefix
	// UploadFailures limits failed upload authorizations per client. If nil,
	// the upload package's default limit applies.
	UploadFailures *ratelimit.Limiter
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

// New returns the handler for every browser-facing route.
func New(cfg Config) (http.Handler, error) {
	uploads, err := upload.New(upload.Config{
		Store:    cfg.Store,
		Pepper:   cfg.Pepper,
		Logger:   cfg.Logger,
		Clients:  clientip.New(cfg.TrustedProxies),
		Failures: cfg.UploadFailures,
	})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", serveIndex)
	mux.HandleFunc("GET "+HTMXPath, serveHTMX)
	mux.Handle("POST /upload", uploads)
	if prefix := HelperPrefix(cfg.Env); prefix != "" && devdata.Allowed(cfg.Env) {
		h := &helpers{store: cfg.Store, pepper: cfg.Pepper, logger: cfg.Logger}
		mux.HandleFunc("POST "+prefix+"/create-account/{game}/{email}/{token}", h.createAccount)
	}
	return http.NewCrossOriginProtection().Handler(mux), nil
}

// helpers are the development and agent account helpers in TESTING.md.
type helpers struct {
	store  *store.Store
	pepper string
	logger *slog.Logger
}

// createAccount inserts an active upload account for an existing game.
// It never modifies an existing row. The token is never logged.
// The game, email, and token are path segments; the credential hash uses the
// game's ID.
func (h *helpers) createAccount(w http.ResponseWriter, r *http.Request) {
	game, email, token := credential.NormalizeGameCode(r.PathValue("game")), credential.NormalizeEmail(r.PathValue("email")), r.PathValue("token")
	err := h.store.CreateUploader(r.Context(), store.NewUploader{
		GameCode: game,
		Email:    email,
		CredentialHash: func(gameID int64) []byte {
			return credential.Hash(h.pepper, email, gameID, token)
		},
	})
	switch {
	case errors.Is(err, store.ErrUnknownGame):
		http.Error(w, "unknown game", http.StatusNotFound)
	case errors.Is(err, store.ErrUploaderExists):
		http.Error(w, "upload account already exists or token is in use", http.StatusConflict)
	case err != nil:
		h.logger.Error("create-account", "game", game, "email", email, "err", err)
		http.Error(w, "could not create the upload account", http.StatusInternalServerError)
	default:
		h.logger.Info("create-account", "game", game, "email", email)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "created upload account %s in %s\n", email, game)
	}
}
