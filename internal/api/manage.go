// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/store"
)

// maxManageBody bounds the body of a management request.
const maxManageBody = 4 << 10

// NewGameRequest is the body of POST /api/games.
type NewGameRequest struct {
	Code string `json:"code"`
}

// Game is the response to a successful POST /api/games.
type Game struct {
	Game string `json:"game"`
}

// UploaderList is the response to GET /api/games/{code}/uploaders.
type UploaderList struct {
	Game      string     `json:"game"`
	Uploaders []Uploader `json:"uploaders"`
}

// Uploader describes one of a game's uploaders. Active is the uploader's
// own flag in this game and AccountActive its account's; it can upload only
// while both, and the game, are active.
type Uploader struct {
	ID            int64  `json:"id"`
	Email         string `json:"email"`
	Active        bool   `json:"active"`
	AccountActive bool   `json:"account_active"`
}

// NewUploaderRequest is the body of POST /api/games/{code}/uploaders.
type NewUploaderRequest struct {
	Email string `json:"email"`
}

// UploaderToken is the response to adding an uploader or resetting its
// token. Token is the new upload token, which cannot be read again.
type UploaderToken struct {
	Game  string `json:"game"`
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Token string `json:"token"`
}

// UploaderStatus is the response to disabling or enabling an uploader.
type UploaderStatus struct {
	Game   string `json:"game"`
	ID     int64  `json:"id"`
	Active bool   `json:"active"`
}

// Messages in the management routes' error responses.
const (
	msgBadJSON        = "the request body must be a JSON object with the documented fields"
	msgBadCode        = "code must be one or more printable ASCII characters, without spaces"
	msgBadEmail       = "email must be one or more printable ASCII characters, without spaces"
	msgGameExists     = "a game with that code already exists"
	msgUploaderExists = "that email is already an uploader of the game"
)

// readJSON decodes the request's JSON body into v, or responds with the
// error.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, msgUnsupported)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxManageBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, msgBadJSON)
		return false
	}
	return true
}

func (h *Handler) createGame(w http.ResponseWriter, r *http.Request, account store.Account) {
	var req NewGameRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !credential.HeaderWord(req.Code) {
		writeError(w, http.StatusBadRequest, msgBadCode)
		return
	}
	code := credential.NormalizeGameCode(req.Code)
	id, err := h.db.CreateManagedGame(r.Context(), code, account.ID)
	if errors.Is(err, store.ErrGameExists) {
		writeError(w, http.StatusConflict, msgGameExists)
		return
	} else if err != nil {
		h.logger.Error("api: create game", "email", account.Email, "game", code, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	h.logger.Info("api: created game", "email", account.Email, "game", code, "game_id", id)
	writeJSON(w, http.StatusCreated, Game{Game: code})
}

func (h *Handler) listUploaders(w http.ResponseWriter, r *http.Request, account store.Account) {
	code, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	uploaders, err := h.db.GameUploaders(r.Context(), gameID)
	if err != nil {
		h.logger.Error("api: list uploaders", "email", account.Email, "game", code, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	list := UploaderList{Game: code, Uploaders: make([]Uploader, 0, len(uploaders))}
	for _, u := range uploaders {
		list.Uploaders = append(list.Uploaders, Uploader{ID: u.ID, Email: u.Email, Active: u.Active, AccountActive: u.AccountActive})
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) addUploader(w http.ResponseWriter, r *http.Request, account store.Account) {
	code, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	var req NewUploaderRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !credential.HeaderWord(req.Email) {
		writeError(w, http.StatusBadRequest, msgBadEmail)
		return
	}
	email := credential.NormalizeEmail(req.Email)
	token := uuid.NewString()
	id, err := h.db.AddGameUploader(r.Context(), gameID, email, credential.Hash(h.pepper, email, gameID, token))
	if errors.Is(err, store.ErrUploaderExists) {
		writeError(w, http.StatusConflict, msgUploaderExists)
		return
	} else if err != nil {
		h.logger.Error("api: add uploader", "email", account.Email, "game", code, "uploader", email, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	h.logger.Info("api: added uploader", "email", account.Email, "game", code, "uploader", email, "uploader_id", id)
	writeJSON(w, http.StatusCreated, UploaderToken{Game: code, ID: id, Email: email, Token: token})
}

// managedUploader returns the request's game and uploader ID if the account
// manages the game, or responds 404. The ID may still name no uploader of
// the game.
func (h *Handler) managedUploader(w http.ResponseWriter, r *http.Request, account store.Account) (code string, gameID, id int64, ok bool) {
	if code, gameID, ok = h.managedGame(w, r, account); !ok {
		return "", 0, 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, msgNotFound)
		return "", 0, 0, false
	}
	return code, gameID, id, true
}

func (h *Handler) setUploaderActive(active bool) func(http.ResponseWriter, *http.Request, store.Account) {
	return func(w http.ResponseWriter, r *http.Request, account store.Account) {
		code, gameID, id, ok := h.managedUploader(w, r, account)
		if !ok {
			return
		}
		found, err := h.db.SetGameUploaderActive(r.Context(), gameID, id, active)
		if err != nil {
			h.logger.Error("api: set uploader active", "email", account.Email, "game", code, "uploader_id", id, "err", err)
			writeError(w, http.StatusInternalServerError, msgServerError)
			return
		} else if !found {
			writeError(w, http.StatusNotFound, msgNotFound)
			return
		}
		h.logger.Info("api: set uploader active", "email", account.Email, "game", code, "uploader_id", id, "active", active)
		writeJSON(w, http.StatusOK, UploaderStatus{Game: code, ID: id, Active: active})
	}
}

func (h *Handler) resetUploaderToken(w http.ResponseWriter, r *http.Request, account store.Account) {
	code, gameID, id, ok := h.managedUploader(w, r, account)
	if !ok {
		return
	}
	token := uuid.NewString()
	email, found, err := h.db.ResetGameUploader(r.Context(), gameID, id, func(email string) []byte {
		return credential.Hash(h.pepper, email, gameID, token)
	})
	if err != nil {
		h.logger.Error("api: reset uploader token", "email", account.Email, "game", code, "uploader_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	} else if !found {
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	}
	h.logger.Info("api: reset uploader token", "email", account.Email, "game", code, "uploader", email, "uploader_id", id)
	writeJSON(w, http.StatusOK, UploaderToken{Game: code, ID: id, Email: email, Token: token})
}
