// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/mdhender/fuingo/internal/store"
)

// TurnList is the response to GET .../turns.
type TurnList struct {
	System string `json:"system"`
	Game   string `json:"game"`
	Turns  []Turn `json:"turns"`
}

// Turn describes one turn of a game. ClosedAt is null while it is open.
type Turn struct {
	Turn      int64   `json:"turn"`
	Closed    bool    `json:"closed"`
	CreatedAt string  `json:"created_at"`
	ClosedAt  *string `json:"closed_at"`
}

// NewTurnRequest is the body of POST .../turns. Turn is required.
type NewTurnRequest struct {
	Turn *int64 `json:"turn"`
}

// TurnStatus is the response to creating, closing, or reopening a turn.
type TurnStatus struct {
	System string `json:"system"`
	Game   string `json:"game"`
	Turn
}

// Messages in the turn routes' error responses.
const (
	msgBadTurn     = "turn must be an integer, 0 or more"
	msgNotNextTurn = "turn must be the game's next turn, which is "
)

func newTurn(t store.Turn) Turn {
	turn := Turn{Turn: t.Number, Closed: t.Closed(), CreatedAt: t.CreatedAt.UTC().Format(TimeLayout)}
	if t.Closed() {
		turn.ClosedAt = new(t.ClosedAt.UTC().Format(TimeLayout))
	}
	return turn
}

func (h *Handler) listTurns(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	turns, err := h.db.Turns(r.Context(), gameID)
	if err != nil {
		h.logger.Error("api: list turns", "email", account.Email, "game", game.String(), "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	list := TurnList{System: game.System, Game: game.Code, Turns: make([]Turn, 0, len(turns))}
	for _, t := range turns {
		list.Turns = append(list.Turns, newTurn(t))
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) createTurn(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	var req NewTurnRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Turn == nil || *req.Turn < 0 {
		writeError(w, http.StatusBadRequest, msgBadTurn)
		return
	}
	turn, err := h.db.CreateTurn(r.Context(), gameID, *req.Turn, h.now())
	if errors.Is(err, store.ErrNotNextTurn) {
		writeError(w, http.StatusConflict, msgNotNextTurn+strconv.FormatInt(turn.Number, 10))
		return
	} else if err != nil {
		h.logger.Error("api: create turn", "email", account.Email, "game", game.String(), "turn", *req.Turn, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	h.logger.Info("api: created turn", "email", account.Email, "game", game.String(), "turn", turn.Number)
	writeJSON(w, http.StatusCreated, TurnStatus{System: game.System, Game: game.Code, Turn: newTurn(turn)})
}

func (h *Handler) setTurnClosed(closed bool) func(http.ResponseWriter, *http.Request, store.Account) {
	return func(w http.ResponseWriter, r *http.Request, account store.Account) {
		game, gameID, ok := h.managedGame(w, r, account)
		if !ok {
			return
		}
		number, ok := pathNumber(r, "turn")
		if !ok {
			writeError(w, http.StatusNotFound, msgNotFound)
			return
		}
		turn, found, err := h.db.SetTurnClosed(r.Context(), gameID, number, closed, h.now())
		if err != nil {
			h.logger.Error("api: set turn closed", "email", account.Email, "game", game.String(), "turn", number, "err", err)
			writeError(w, http.StatusInternalServerError, msgServerError)
			return
		} else if !found {
			writeError(w, http.StatusNotFound, msgNotFound)
			return
		}
		h.logger.Info("api: set turn closed", "email", account.Email, "game", game.String(), "turn", number, "closed", closed)
		writeJSON(w, http.StatusOK, TurnStatus{System: game.System, Game: game.Code, Turn: newTurn(turn)})
	}
}
