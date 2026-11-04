// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/gmstore"
	"github.com/mdhender/fuingo/internal/sqlitedb"
	"github.com/mdhender/fuingo/internal/store"
)

// newStoreFile creates a store file and returns the arguments that select it.
func newStoreFile(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "olympia.db")
	if _, _, err := runOlympia(t, "--env", "test", "--store", path, "store", "create"); err != nil {
		t.Fatal(err)
	}
	return []string{"--env", "test", "--store", path}
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, _, err := runOlympia(t, args...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out
}

func TestStoreCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "olympia.db")
	if _, _, err := runOlympia(t, "--env", "test", "--store", path, "game", "list"); err == nil {
		t.Error("game list created or opened a missing store")
	}
	mustRun(t, "--env", "test", "--store", path, "store", "create")
	if _, _, err := runOlympia(t, "--env", "test", "--store", path, "store", "create"); !errors.Is(err, sqlitedb.ErrDatabaseExists) {
		t.Errorf("second create: %v", err)
	}
	if out := mustRun(t, "--env", "test", "--store", path, "store", "migrate"); !strings.Contains(out, "schema version 1") {
		t.Errorf("migrate: %q", out)
	}
	// The store comes from OLYMPIA_STORE too.
	t.Setenv("OLYMPIA_STORE", path)
	mustRun(t, "--env", "test", "game", "add", "alpha")
	if _, _, err := runOlympia(t, "--env", "test", "--store", ":memory:", "game", "list"); !errors.Is(err, ErrMemoryStore) {
		t.Errorf(":memory: = %v", err)
	}
	// A fuingo database is refused.
	fu := filepath.Join(dir, "fuingo.db")
	s, err := store.Create(t.Context(), fu)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, _, err := runOlympia(t, "--env", "test", "--store", fu, "game", "list"); !errors.Is(err, sqlitedb.ErrNotOwned) {
		t.Errorf("fuingo database: %v", err)
	}
}

func TestGameCommands(t *testing.T) {
	st := newStoreFile(t)
	run := func(args ...string) (string, error) {
		out, _, err := runOlympia(t, append(append([]string{}, st...), args...)...)
		return out, err
	}
	if _, err := run("game", "add", "Alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("game", "add", "alpha"); !errors.Is(err, gmstore.ErrGameExists) {
		t.Errorf("duplicate: %v", err)
	}
	for _, bad := range []string{"../x", "-x", "a b", ""} {
		if _, err := run("game", "add", bad); err == nil {
			t.Errorf("game add %q: no error", bad)
		}
	}
	if _, err := run("game", "add"); !errors.Is(err, ErrUsage) {
		t.Errorf("no code: %v", err)
	}
	run("game", "add", "beta")
	run("player", "add", "--game", "beta", "--faction", "ab1", "--uploader", "a@example.com")
	out, _ := run("game", "list")
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") || !strings.Contains(out, "1") {
		t.Errorf("list = %q", out)
	}
	if _, err := run("game", "rename", "beta", "alpha"); !errors.Is(err, gmstore.ErrGameExists) {
		t.Errorf("rename onto existing: %v", err)
	}
	if _, err := run("game", "rename", "beta", "gamma"); err != nil {
		t.Fatal(err)
	}
	if out, err := run("player", "list", "--game", "gamma"); err != nil || !strings.Contains(out, "ab1") {
		t.Errorf("players follow rename: %q %v", out, err)
	}
	run("game", "disable", "gamma")
	if out, _ := run("game", "show", "gamma"); !strings.Contains(out, "disabled") {
		t.Errorf("show = %q", out)
	}
	if _, err := run("game", "enable", "gamma"); err != nil {
		t.Error(err)
	}
	if _, err := run("game", "delete", "gamma"); !errors.Is(err, gmstore.ErrGameInUse) {
		t.Errorf("delete with players: %v", err)
	}
	if _, err := run("game", "delete", "alpha"); err != nil {
		t.Error(err)
	}
	if _, err := run("game", "show", "alpha"); !errors.Is(err, gmstore.ErrUnknownGame) {
		t.Errorf("show deleted: %v", err)
	}
}

func TestPlayerCommands(t *testing.T) {
	st := newStoreFile(t)
	run := func(args ...string) (string, error) {
		out, _, err := runOlympia(t, append(append([]string{}, st...), args...)...)
		return out, err
	}
	run("game", "add", "alpha")
	if _, err := run("player", "add", "--game", "ALPHA", "--faction", "AB1", "--uploader", "Alice@Example.com", "--name", "Alice's Elves"); err != nil {
		t.Fatal(err)
	}
	// The same uploader may play a second faction.
	if _, err := run("player", "add", "--game", "alpha", "--faction", "cd2", "--uploader", "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("player", "add", "--game", "alpha", "--faction", "ab1", "--uploader", "bob@example.com"); !errors.Is(err, gmstore.ErrPlayerExists) {
		t.Errorf("duplicate faction: %v", err)
	}
	if _, err := run("player", "add", "--game", "nope", "--faction", "ef3", "--uploader", "bob@example.com"); !errors.Is(err, gmstore.ErrUnknownGame) {
		t.Errorf("unknown game: %v", err)
	}
	for name, args := range map[string][]string{
		"bad faction":  {"--game", "alpha", "--faction", "a-1", "--uploader", "b@example.com"},
		"bad email":    {"--game", "alpha", "--faction", "ef3", "--uploader", "b @example.com"},
		"no uploader":  {"--game", "alpha", "--faction", "ef3"},
		"no faction":   {"--game", "alpha", "--uploader", "b@example.com"},
		"control name": {"--game", "alpha", "--faction", "ef3", "--uploader", "b@example.com", "--name", "a\tb"},
	} {
		if _, err := run(append([]string{"player", "add"}, args...)...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	out, err := run("player", "show", "--game", "alpha", "--faction", "ab1")
	if err != nil || !strings.Contains(out, "alice@example.com") || !strings.Contains(out, "Alice's Elves") {
		t.Errorf("show = %q, %v", out, err)
	}

	if _, err := run("player", "update", "--game", "alpha", "--faction", "ab1"); !errors.Is(err, ErrNoChange) {
		t.Errorf("update without changes: %v", err)
	}
	if _, err := run("player", "update", "--game", "alpha", "--faction", "ab1", "--new-faction", "cd2"); !errors.Is(err, gmstore.ErrPlayerExists) {
		t.Errorf("update onto existing faction: %v", err)
	}
	if _, err := run("player", "update", "--game", "alpha", "--faction", "zz9", "--name", "x"); !errors.Is(err, gmstore.ErrUnknownPlayer) {
		t.Errorf("update unknown: %v", err)
	}
	if _, err := run("player", "update", "--game", "alpha", "--faction", "ab1", "--new-faction", "gh4", "--uploader", "Carol@example.com", "--name", ""); err != nil {
		t.Fatal(err)
	}
	out, _ = run("player", "show", "--game", "alpha", "--faction", "gh4")
	if !strings.Contains(out, "carol@example.com") || strings.Contains(out, "Elves") {
		t.Errorf("after update = %q", out)
	}

	run("player", "disable", "--game", "alpha", "--faction", "gh4")
	run("player", "disable", "--game", "alpha", "--faction", "gh4")
	out, _ = run("player", "list", "--game", "alpha")
	if !strings.Contains(out, "cd2") || !strings.Contains(out, "disabled") {
		t.Errorf("list = %q", out)
	}
	if _, err := run("player", "delete", "--game", "alpha", "--faction", "gh4"); err != nil {
		t.Error(err)
	}
	if _, err := run("player", "show", "--game", "alpha", "--faction", "gh4"); !errors.Is(err, gmstore.ErrUnknownPlayer) {
		t.Errorf("show deleted: %v", err)
	}
}

func TestPlayerFlagsAreCommandLineOnly(t *testing.T) {
	st := newStoreFile(t)
	run := func(args ...string) error {
		_, _, err := runOlympia(t, append(append([]string{}, st...), args...)...)
		return err
	}
	run("game", "add", "alpha")
	t.Setenv("OLYMPIA_GAME", "alpha")
	t.Setenv("OLYMPIA_FACTION", "ab1")
	t.Setenv("OLYMPIA_UPLOADER", "a@example.com")
	t.Setenv("OLYMPIA_NAME", "From the environment")
	if err := run("player", "add"); err == nil {
		t.Error("player add took its arguments from the environment")
	}
	if err := run("player", "add", "--game", "alpha", "--faction", "ab1", "--uploader", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	out, _, _ := runOlympia(t, append(append([]string{}, st...), "player", "show", "--game", "alpha", "--faction", "ab1")...)
	if strings.Contains(out, "From the environment") {
		t.Errorf("OLYMPIA_NAME was honored: %q", out)
	}
}
