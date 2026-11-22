// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package devdata defines the default system, games, players, accounts, and
// game managers that seed new databases in the development and agent
// environments.
//
// The passwords are fixed, public test values, not secrets. Keep TESTING.md
// in sync with this file.
package devdata

import (
	"context"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	ErrNotAllowed = cerrs.Error("default accounts exist only in development and agent")
)

// System is the code of the default system, which holds every default game.
const System = "system01"

// Games lists the default game codes. Every default game is active.
var Games = []string{"game01", "game02"}

// Player is a default player: an account without a password, in one game.
type Player struct {
	Game  string
	Email string
}

// Players lists the default players. Every one is active.
var Players = []Player{
	{Game: "game01", Email: "catbird@example.com"},
	{Game: "game01", Email: "spoonbill@example.com"},
	{Game: "game02", Email: "caracara@example.com"},
}

// Account is a default account with a password, and the games it manages.
type Account struct {
	Email    string
	Password string
	Games    []string
}

// Accounts lists the default accounts with passwords.
var Accounts = []Account{
	{Email: "acct01@example.com", Password: "worser-choices-2001", Games: []string{"game01"}},
	{Email: "acct02@example.com", Password: "worser-choices-2002", Games: []string{"game02"}},
}

// Allowed reports whether env may have default accounts and helpers.
func Allowed(env string) bool {
	return env == "development" || env == "agent"
}

// Seed inserts the default system, games, players, accounts, and game
// managers into a new database, hashing the passwords with bcrypt at
// bcrypt.MinCost. It refuses environments other than development and agent,
// and fails without changes if any default already exists, so it never
// re-seeds a database.
func Seed(ctx context.Context, s *store.Store, env string) error {
	if !Allowed(env) {
		return ErrNotAllowed
	}
	rows := store.Rows{Systems: []string{System}}
	for _, code := range Games {
		rows.Games = append(rows.Games, store.GameRef{System: System, Code: code})
	}
	for _, p := range Players {
		rows.Players = append(rows.Players, store.Member{Game: store.GameRef{System: System, Code: p.Game}, Email: p.Email})
	}
	for _, a := range Accounts {
		for _, game := range a.Games {
			rows.Managers = append(rows.Managers, store.Member{Game: store.GameRef{System: System, Code: game}, Email: a.Email})
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(a.Password), bcrypt.MinCost)
		if err != nil {
			return err
		}
		rows.Accounts = append(rows.Accounts, store.NewAccount{Email: a.Email, HashedSecret: hash})
	}
	return s.CreateRows(ctx, rows)
}
