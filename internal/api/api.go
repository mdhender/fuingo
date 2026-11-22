// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package api implements the /api/ routes for game masters and their tools.
//
// A client logs in with an account's email and password at POST /api/login
// and receives a bearer token, which it sends as "Authorization: Bearer
// TOKEN" on every other route. Only the token's SHA-256 is stored.
//
// A game is named by its system and its own code, as in
// /api/systems/{system}/games/{game}. Every game route is scoped to the
// games the account actively manages. A game it does not manage, and
// anything in such a game, get the same 404 as one that does not exist.
//
// Failed logins are limited per client address.
//
// Every error is a JSON Error, including a path or method that matches no
// route. The server uses Forbidden for cross-origin requests it refuses.
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	// SessionLifetime is how long a bearer token from /api/login is valid.
	SessionLifetime = 24 * time.Hour
	// FailureBurst is how many failed logins a client may make before it
	// is limited.
	FailureBurst = 5
	// FailureInterval is how often a limited client regains one attempt.
	FailureInterval = time.Minute
	// TimeLayout formats every timestamp in responses: UTC, at a fixed width.
	TimeLayout = "2006-01-02T15:04:05.000000000Z"
	// maxLoginBody bounds the login request.
	maxLoginBody = 4 << 10
)

// LoginRequest is the body of POST /api/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is the response to a successful login.
type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// Error is the body of every error response.
type Error struct {
	Error string `json:"error"`
}

// Messages in error responses.
const (
	msgForbidden        = "cross-origin requests are not allowed"
	msgMethodNotAllowed = "method not allowed"
	msgBadRequest       = "the request body must be a JSON object with email and password"
	msgLoginFailed      = "the email or password is not valid"
	msgNotFound         = "not found"
	msgServerError      = "the request could not be completed; try again later"
	msgTooManyFailed    = "too many failed logins from your address; try again later"
	msgUnauthorized     = "a valid bearer token is required"
	msgUnsupported      = "the request body must be application/json"
)

// Config configures a Handler.
type Config struct {
	Store  *store.Store
	Logger *slog.Logger
	// Clients finds the client's address behind trusted proxies.
	Clients clientip.Resolver
	// LoginFailures limits failed logins per client. If nil, each client may
	// fail FailureBurst times, then once per FailureInterval.
	LoginFailures *ratelimit.Limiter
	// Now returns the current time. If nil, it is time.Now.
	Now func() time.Time
}

// Handler serves the /api/ routes.
type Handler struct {
	db       *store.Store
	logger   *slog.Logger
	clients  clientip.Resolver
	failures *ratelimit.Limiter
	now      func() time.Time
	mux      *http.ServeMux
	// dummyHash is compared against when an account has no usable password,
	// so a failed login takes the same time whatever the reason.
	dummyHash []byte
}

// New returns the handler for the /api/ routes.
func New(cfg Config) (*Handler, error) {
	if cfg.LoginFailures == nil {
		cfg.LoginFailures = ratelimit.New(FailureBurst, FailureInterval, nil)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), bcrypt.MinCost)
	if err != nil {
		return nil, err
	}
	h := &Handler{db: cfg.Store, logger: cfg.Logger, clients: cfg.Clients, failures: cfg.LoginFailures, now: cfg.Now, dummyHash: dummy}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("GET /api/games", h.authenticated(h.listGames))
	const game = "/api/systems/{system}/games/{game}"
	mux.HandleFunc("GET "+game+"/players", h.authenticated(h.listPlayers))
	mux.HandleFunc("POST "+game+"/players", h.authenticated(h.addPlayer))
	mux.HandleFunc("POST "+game+"/players/{id}/disable", h.authenticated(h.setPlayerActive(false)))
	mux.HandleFunc("POST "+game+"/players/{id}/enable", h.authenticated(h.setPlayerActive(true)))
	mux.HandleFunc("GET "+game+"/turns", h.authenticated(h.listTurns))
	mux.HandleFunc("POST "+game+"/turns", h.authenticated(h.createTurn))
	mux.HandleFunc("POST "+game+"/turns/{turn}/close", h.authenticated(h.setTurnClosed(true)))
	mux.HandleFunc("POST "+game+"/turns/{turn}/open", h.authenticated(h.setTurnClosed(false)))
	h.mux = mux
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	next, pattern := h.mux.Handler(r)
	if pattern != "" {
		h.mux.ServeHTTP(w, r)
		return
	}
	// No route matches: the mux would answer in plain text. Keep its status
	// and headers, such as Allow, and answer in JSON like every other error.
	status := statusOnly{ResponseWriter: w, code: http.StatusNotFound}
	next.ServeHTTP(&status, r)
	if status.code == http.StatusMethodNotAllowed {
		writeError(w, status.code, msgMethodNotAllowed)
		return
	}
	writeError(w, http.StatusNotFound, msgNotFound)
}

// Forbidden answers a cross-origin request that was refused before it
// reached a route, in JSON like every other error.
func Forbidden(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusForbidden, msgForbidden)
}

// statusOnly records the status a handler writes and discards the rest,
// leaving the headers it set on the underlying writer.
type statusOnly struct {
	http.ResponseWriter
	code int
}

func (s *statusOnly) WriteHeader(code int)        { s.code = code }
func (s *statusOnly) Write(p []byte) (int, error) { return len(p), nil }

// IssueToken creates a session for the account and returns its bearer
// token and expiry. The token is returned once and never stored or logged.
func (h *Handler) IssueToken(r *http.Request, accountID int64) (LoginResponse, error) {
	token := rand.Text()
	sum := sha256.Sum256([]byte(token))
	now := h.now()
	expires := now.Add(SessionLifetime)
	if err := h.db.CreateSession(r.Context(), accountID, sum[:], now, expires); err != nil {
		return LoginResponse{}, err
	}
	return LoginResponse{Token: token, ExpiresAt: expires.UTC().Format(TimeLayout)}, nil
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, msgUnsupported)
		return
	}
	var req LoginRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody))
	dec.DisallowUnknownFields()
	if err := decodeOnly(dec, &req); err != nil || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, msgBadRequest)
		return
	}

	// A failure is reserved before the check, so concurrent guesses cannot
	// exceed the client's limit, and given back unless the login fails.
	client := h.clients.Addr(r)
	if ok, wait := h.failures.Take(client); !ok {
		h.logger.Warn("api login: too many failures", "client", client)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, msgTooManyFailed)
		return
	}
	// An email that could not belong to an account fails like a wrong
	// password, after the same bcrypt comparison.
	email, emailErr := names.Email(req.Email)
	account, found, err := h.db.FindAccount(r.Context(), email)
	if err != nil {
		h.failures.Return(client)
		h.logger.Error("api login: find account", "client", client, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	hash := h.dummyHash
	usable := emailErr == nil && found && account.Active && account.HashedSecret != nil
	if usable {
		hash = account.HashedSecret
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) != nil || !usable {
		h.logger.Info("api login failed", "client", client)
		writeError(w, http.StatusUnauthorized, msgLoginFailed)
		return
	}
	h.failures.Return(client)
	resp, err := h.IssueToken(r, account.ID)
	if err != nil {
		h.logger.Error("api login: create session", "client", client, "email", email, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	h.logger.Info("api login", "client", client, "email", email)
	writeJSON(w, http.StatusOK, resp)
}

// authenticated runs next with the account of the request's bearer token,
// or responds 401.
func (h *Handler) authenticated(next func(http.ResponseWriter, *http.Request, store.Account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		token = strings.TrimSpace(token)
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			unauthorized(w)
			return
		}
		sum := sha256.Sum256([]byte(token))
		account, found, err := h.db.SessionAccount(r.Context(), sum[:], h.now())
		if err != nil {
			h.logger.Error("api: find session", "err", err)
			writeError(w, http.StatusInternalServerError, msgServerError)
			return
		} else if !found {
			unauthorized(w)
			return
		}
		next(w, r, account)
	}
}

// managedGame returns the request's game and its ID if the account manages
// it, or responds 404. A system or game code that could never be valid
// names no game.
func (h *Handler) managedGame(w http.ResponseWriter, r *http.Request, account store.Account) (store.GameRef, int64, bool) {
	system, err1 := names.Code(r.PathValue("system"))
	code, err2 := names.Code(r.PathValue("game"))
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusNotFound, msgNotFound)
		return store.GameRef{}, 0, false
	}
	game := store.GameRef{System: system, Code: code}
	id, ok, err := h.db.ManagedGameID(r.Context(), account.ID, game)
	if err != nil {
		h.logger.Error("api: find managed game", "email", account.Email, "game", game.String(), "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return store.GameRef{}, 0, false
	} else if !ok {
		writeError(w, http.StatusNotFound, msgNotFound)
		return store.GameRef{}, 0, false
	}
	return game, id, true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="fuingo"`)
	writeError(w, http.StatusUnauthorized, msgUnauthorized)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, Error{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
