// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/store"
)

// maxManageBody bounds the body of a management request.
const maxManageBody = 4 << 10

// GameList is the response to GET /api/games.
type GameList struct {
	Games []ManagedGame `json:"games"`
}

// ManagedGame is a game the account manages, named by its system and its
// code. An inactive game can still be managed.
type ManagedGame struct {
	System string `json:"system"`
	Code   string `json:"code"`
	Active bool   `json:"active"`
}

// PlayerList is the response to GET .../players.
type PlayerList struct {
	System  string   `json:"system"`
	Game    string   `json:"game"`
	Players []Player `json:"players"`
}

// Player describes one of a game's players. ID is the player's number.
// Active is the player's own flag in this game and AccountActive its
// account's; the player can use the game only while both, and the game, are
// active.
type Player struct {
	ID            int64  `json:"id"`
	Email         string `json:"email"`
	Active        bool   `json:"active"`
	AccountActive bool   `json:"account_active"`
}

// NewPlayerRequest is the body of POST .../players.
type NewPlayerRequest struct {
	Email string `json:"email"`
}

// NewPlayer is the response to adding a player.
type NewPlayer struct {
	System string `json:"system"`
	Game   string `json:"game"`
	ID     int64  `json:"id"`
	Email  string `json:"email"`
}

// PlayerStatus is the response to disabling or enabling a player.
type PlayerStatus struct {
	System string `json:"system"`
	Game   string `json:"game"`
	ID     int64  `json:"id"`
	Active bool   `json:"active"`
}

// Messages in the management routes' error responses.
const (
	msgBadJSON      = "the request body must be a JSON object with the documented fields"
	msgBadEmail     = "email must be one or more printable ASCII characters, without spaces"
	msgPlayerExists = "that email is already a player in the game"
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
	if err := decodeOnly(dec, v); err != nil {
		writeError(w, http.StatusBadRequest, msgBadJSON)
		return false
	}
	return true
}

// errTrailingData is returned by decodeOnly for a body with anything but
// white space after its JSON value.
const errTrailingData = cerrs.Error("data after the JSON value")

// decodeOnly decodes the single JSON value that must make up the whole
// body into v.
func decodeOnly(dec *json.Decoder, v any) error {
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errTrailingData
	}
	return nil
}

func (h *Handler) listGames(w http.ResponseWriter, r *http.Request, account store.Account) {
	games, err := h.db.ManagedGames(r.Context(), account.ID)
	if err != nil {
		h.logger.Error("api: list games", "email", account.Email, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	list := GameList{Games: make([]ManagedGame, 0, len(games))}
	for _, g := range games {
		list.Games = append(list.Games, ManagedGame{System: g.Game.System, Code: g.Game.Code, Active: g.Active})
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) listPlayers(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	players, err := h.db.GamePlayers(r.Context(), gameID)
	if err != nil {
		h.logger.Error("api: list players", "email", account.Email, "game", game.String(), "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	list := PlayerList{System: game.System, Game: game.Code, Players: make([]Player, 0, len(players))}
	for _, p := range players {
		list.Players = append(list.Players, Player{ID: p.ID, Email: p.Email, Active: p.Active, AccountActive: p.AccountActive})
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) addPlayer(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	var req NewPlayerRequest
	if !readJSON(w, r, &req) {
		return
	}
	email, err := names.Email(req.Email)
	if err != nil {
		writeError(w, http.StatusBadRequest, msgBadEmail)
		return
	}
	id, err := h.db.AddGamePlayer(r.Context(), gameID, email)
	if errors.Is(err, store.ErrPlayerExists) {
		writeError(w, http.StatusConflict, msgPlayerExists)
		return
	} else if err != nil {
		h.logger.Error("api: add player", "email", account.Email, "game", game.String(), "player", email, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	h.logger.Info("api: added player", "email", account.Email, "game", game.String(), "player", email, "player_id", id)
	writeJSON(w, http.StatusCreated, NewPlayer{System: game.System, Game: game.Code, ID: id, Email: email})
}

func (h *Handler) setPlayerActive(active bool) func(http.ResponseWriter, *http.Request, store.Account) {
	return func(w http.ResponseWriter, r *http.Request, account store.Account) {
		game, gameID, ok := h.managedGame(w, r, account)
		if !ok {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, msgNotFound)
			return
		}
		found, err := h.db.SetGamePlayerActive(r.Context(), gameID, id, active)
		if err != nil {
			h.logger.Error("api: set player active", "email", account.Email, "game", game.String(), "player_id", id, "err", err)
			writeError(w, http.StatusInternalServerError, msgServerError)
			return
		} else if !found {
			writeError(w, http.StatusNotFound, msgNotFound)
			return
		}
		h.logger.Info("api: set player active", "email", account.Email, "game", game.String(), "player_id", id, "active", active)
		writeJSON(w, http.StatusOK, PlayerStatus{System: game.System, Game: game.Code, ID: id, Active: active})
	}
}
