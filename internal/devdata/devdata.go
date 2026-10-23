// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package devdata defines the default games and upload accounts that seed new
// databases in the development and agent environments.
//
// The tokens are fixed, public test values, not secrets. Keep TESTING.md in
// sync with this file.
package devdata

import (
	"context"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	ErrMissingPepper = cerrs.Error("FUINGO_PEPPER is missing or empty")
	ErrNotAllowed    = cerrs.Error("default accounts exist only in development and agent")
)

// Games lists the default game codes. Every default game is active.
var Games = []string{"game01", "game02"}

// Uploader is a default upload account.
type Uploader struct {
	Game  string
	Email string
	Token string
}

// Uploaders lists the default upload accounts. Every one is active.
var Uploaders = []Uploader{
	{Game: "game01", Email: "catbird@example.com", Token: "8eb87beb-0869-4815-8ce8-afeef14da57e"},
	{Game: "game01", Email: "spoonbill@example.com", Token: "942a563c-c911-4c84-848b-fe4e96122d8c"},
	{Game: "game02", Email: "caracara@example.com", Token: "12fc952a-2b17-49e5-ba31-329a3ba474e4"},
}

// Allowed reports whether env may have default accounts and helpers.
func Allowed(env string) bool {
	return env == "development" || env == "agent"
}

// Seed inserts the default games and upload accounts into a new database,
// hashing the tokens with pepper. It refuses environments other than
// development and agent, and fails without changes if any default already
// exists, so it never re-seeds a database.
func Seed(ctx context.Context, s *store.Store, env, pepper string) error {
	if !Allowed(env) {
		return ErrNotAllowed
	}
	if pepper == "" {
		return ErrMissingPepper
	}
	var uploaders []store.NewUploader
	for _, u := range Uploaders {
		uploaders = append(uploaders, store.NewUploader{
			GameCode: u.Game,
			Email:    credential.NormalizeEmail(u.Email),
			CredentialHash: func(gameID int64) []byte {
				return credential.Hash(pepper, u.Email, gameID, u.Token)
			},
		})
	}
	return s.CreateGamesAndUploaders(ctx, Games, uploaders)
}
