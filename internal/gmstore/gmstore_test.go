// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmstore_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/mdhender/fuingo/internal/gmstore"
	"github.com/mdhender/fuingo/internal/sqlitedb"
	"github.com/mdhender/fuingo/internal/store"
)

func newStore(t *testing.T) *gmstore.Store {
	t.Helper()
	s, err := gmstore.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ptr(s string) *string { return &s }

func TestFilesAreNotInterchangeableWithFuingo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gm := filepath.Join(dir, "olympia.db")
	s, err := gmstore.Create(t.Context(), gm)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := gmstore.Create(t.Context(), gm); !errors.Is(err, sqlitedb.ErrDatabaseExists) {
		t.Errorf("second create: %v", err)
	}
	if err := gmstore.Migrate(t.Context(), gm); err != nil {
		t.Errorf("migrate: %v", err)
	}
	if _, err := store.Open(t.Context(), gm); !errors.Is(err, sqlitedb.ErrNotOwned) {
		t.Errorf("fuingo opened an olympia store: %v", err)
	}
	fu := filepath.Join(dir, "fuingo.db")
	fs, err := store.Create(t.Context(), fu)
	if err != nil {
		t.Fatal(err)
	}
	fs.Close()
	if _, err := gmstore.Open(t.Context(), fu); !errors.Is(err, sqlitedb.ErrNotOwned) {
		t.Errorf("olympia opened a fuingo database: %v", err)
	}
	if _, err := gmstore.Open(t.Context(), filepath.Join(dir, "missing.db")); err == nil {
		t.Error("opened a missing file")
	}
}

func TestGames(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	for _, code := range []string{"beta", "alpha"} {
		if err := s.AddGame(ctx, code); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddGame(ctx, "alpha"); !errors.Is(err, gmstore.ErrGameExists) {
		t.Errorf("duplicate: %v", err)
	}
	if err := s.AddGame(ctx, "Alpha"); err == nil {
		t.Error("uppercase code accepted")
	}
	games, err := s.Games(ctx)
	if err != nil || len(games) != 2 || games[0].Code != "alpha" || !games[0].Active {
		t.Fatalf("games = %+v, %v", games, err)
	}
	if err := s.RenameGame(ctx, "beta", "alpha"); !errors.Is(err, gmstore.ErrGameExists) {
		t.Errorf("rename onto existing: %v", err)
	}
	if err := s.RenameGame(ctx, "nope", "x"); !errors.Is(err, gmstore.ErrUnknownGame) {
		t.Errorf("rename unknown: %v", err)
	}
	if err := s.AddPlayer(ctx, gmstore.Player{GameCode: "beta", Faction: "ab1", Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameGame(ctx, "beta", "gamma"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Player(ctx, "gamma", "ab1"); err != nil || p.GameCode != "gamma" {
		t.Errorf("player after rename = %+v, %v", p, err)
	}
	for range 2 {
		if err := s.SetGameActive(ctx, "gamma", false); err != nil {
			t.Fatal(err)
		}
	}
	if g, _ := s.Game(ctx, "gamma"); g.Active || g.Players != 1 {
		t.Errorf("game = %+v", g)
	}
	if err := s.SetGameActive(ctx, "nope", true); !errors.Is(err, gmstore.ErrUnknownGame) {
		t.Errorf("enable unknown: %v", err)
	}
	if err := s.DeleteGame(ctx, "gamma"); !errors.Is(err, gmstore.ErrGameInUse) {
		t.Errorf("delete game with players: %v", err)
	}
	if err := s.DeleteGame(ctx, "alpha"); err != nil {
		t.Errorf("delete unused game: %v", err)
	}
	if err := s.DeleteGame(ctx, "alpha"); !errors.Is(err, gmstore.ErrUnknownGame) {
		t.Errorf("delete again: %v", err)
	}
}

func TestPlayers(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	s.AddGame(ctx, "alpha")
	s.AddGame(ctx, "beta")
	add := func(game, faction, email string) error {
		return s.AddPlayer(ctx, gmstore.Player{GameCode: game, Faction: faction, Email: email, Name: "N " + faction})
	}
	// One email may play several factions, and a faction code may recur in
	// another game.
	for _, p := range [][3]string{{"alpha", "ab1", "a@example.com"}, {"alpha", "cd2", "a@example.com"}, {"alpha", "ef3", "b@example.com"}, {"beta", "ab1", "a@example.com"}} {
		if err := add(p[0], p[1], p[2]); err != nil {
			t.Fatalf("%v: %v", p, err)
		}
	}
	if err := add("alpha", "ab1", "c@example.com"); !errors.Is(err, gmstore.ErrPlayerExists) {
		t.Errorf("duplicate faction: %v", err)
	}
	if err := add("nope", "ab1", "c@example.com"); !errors.Is(err, gmstore.ErrUnknownGame) {
		t.Errorf("unknown game: %v", err)
	}
	players, err := s.Players(ctx, "alpha")
	if err != nil || len(players) != 3 || players[0].Faction != "ab1" || players[0].Name != "N ab1" {
		t.Fatalf("players = %+v, %v", players, err)
	}
	if _, err := s.Players(ctx, "nope"); !errors.Is(err, gmstore.ErrUnknownGame) {
		t.Errorf("players of unknown game: %v", err)
	}
	if _, err := s.Player(ctx, "alpha", "zz9"); !errors.Is(err, gmstore.ErrUnknownPlayer) {
		t.Errorf("unknown player: %v", err)
	}

	if err := s.UpdatePlayer(ctx, "alpha", "ab1", gmstore.PlayerChange{Email: ptr("c@example.com")}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Player(ctx, "alpha", "ab1"); p.Email != "c@example.com" || p.Name != "N ab1" {
		t.Errorf("after email change = %+v", p)
	}
	if err := s.UpdatePlayer(ctx, "alpha", "ab1", gmstore.PlayerChange{Faction: ptr("cd2")}); !errors.Is(err, gmstore.ErrPlayerExists) {
		t.Errorf("faction onto existing: %v", err)
	}
	if err := s.UpdatePlayer(ctx, "alpha", "ab1", gmstore.PlayerChange{Faction: ptr("gh4"), Name: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Player(ctx, "alpha", "gh4"); err != nil || p.Name != "" {
		t.Errorf("after faction change = %+v, %v", p, err)
	}
	if err := s.UpdatePlayer(ctx, "alpha", "ab1", gmstore.PlayerChange{Name: ptr("x")}); !errors.Is(err, gmstore.ErrUnknownPlayer) {
		t.Errorf("update old faction: %v", err)
	}
	// The other game's ab1 is untouched.
	if p, _ := s.Player(ctx, "beta", "ab1"); p.Email != "a@example.com" {
		t.Errorf("beta ab1 = %+v", p)
	}

	for range 2 {
		if err := s.SetPlayerActive(ctx, "alpha", "gh4", false); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := s.Player(ctx, "alpha", "gh4"); p.Active {
		t.Error("player still active")
	}
	if err := s.DeletePlayer(ctx, "alpha", "gh4"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePlayer(ctx, "alpha", "gh4"); !errors.Is(err, gmstore.ErrUnknownPlayer) {
		t.Errorf("delete again: %v", err)
	}
}
