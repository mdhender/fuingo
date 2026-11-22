// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/store"
)

// clearEnv keeps the developer's environment out of the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FUINGO_ENV", "FUINGO_DB"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func runCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), args, &stdout, &stderr)
	return stdout.String() + stderr.String(), err
}

// defaultPlayers reports how many default players the database holds.
func defaultPlayers(t *testing.T, path string) int {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	n := 0
	for _, code := range devdata.Games {
		players, err := s.Players(t.Context(), store.GameRef{System: devdata.System, Code: code})
		if err != nil {
			t.Fatal(err)
		}
		n += len(players)
	}
	return n
}

func TestCreateSeedsDefaultsInDevelopmentAndAgent(t *testing.T) {
	clearEnv(t)
	for _, env := range []string{"development", "agent"} {
		path := filepath.Join(t.TempDir(), "fuingo.db")
		if out, err := runCommand(t, "--env", env, "create", "--db", path); err != nil {
			t.Fatalf("%s: create: %v\n%s", env, err, out)
		}
		if n := defaultPlayers(t, path); n != len(devdata.Players) {
			t.Errorf("%s: %d default players, want %d", env, n, len(devdata.Players))
		}
	}
}

func TestCreateDoesNotSeedOtherEnvironments(t *testing.T) {
	clearEnv(t)
	for _, env := range []string{"test", "production"} {
		path := filepath.Join(t.TempDir(), "fuingo.db")
		if out, err := runCommand(t, "--env", env, "create", "--db", path); err != nil {
			t.Fatalf("%s: create: %v\n%s", env, err, out)
		}
		if n := defaultPlayers(t, path); n != 0 {
			t.Errorf("%s: %d default players, want 0", env, n)
		}
	}
}

func TestCreateRefusesExistingFile(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "fuingo.db")
	if _, err := runCommand(t, "--env", "development", "create", "--db", path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := runCommand(t, "--env", "development", "create", "--db", path); !errors.Is(err, store.ErrDatabaseExists) {
		t.Errorf("second create = %v, want ErrDatabaseExists", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("second create modified the database")
	}
}

func TestDatabaseFlagIsRequiredAndNotMemory(t *testing.T) {
	clearEnv(t)
	for _, sub := range []string{"create", "migrate"} {
		if _, err := runCommand(t, "--env", "development", sub); !errors.Is(err, ErrMissingDatabase) {
			t.Errorf("%s without --db = %v, want ErrMissingDatabase", sub, err)
		}
		if _, err := runCommand(t, "--env", "development", sub, "--db", ":memory:"); !errors.Is(err, ErrMemoryDatabase) {
			t.Errorf("%s --db :memory: = %v, want ErrMemoryDatabase", sub, err)
		}
	}
}

func TestEnvironmentIsRequired(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "fuingo.db")
	if _, err := runCommand(t, "create", "--db", path); err == nil {
		t.Errorf("create without an environment succeeded")
	}
	if _, err := runCommand(t, "--env", "staging", "create", "--db", path); err == nil {
		t.Errorf("create with an unknown environment succeeded")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("create made %s", path)
	}
}

func TestEnvironmentVariablesAndFlagPrecedence(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	fromEnv := filepath.Join(dir, "env.db")
	fromFlag := filepath.Join(dir, "flag.db")
	t.Setenv("FUINGO_ENV", "development")
	t.Setenv("FUINGO_DB", fromEnv)

	if out, err := runCommand(t, "create"); err != nil {
		t.Fatalf("create from environment: %v\n%s", err, out)
	}
	if n := defaultPlayers(t, fromEnv); n != len(devdata.Players) {
		t.Errorf("FUINGO_ENV=development did not seed defaults")
	}

	// Explicit flags override the environment.
	if out, err := runCommand(t, "--env", "production", "create", "--db", fromFlag); err != nil {
		t.Fatalf("create with flags: %v\n%s", err, out)
	}
	if n := defaultPlayers(t, fromFlag); n != 0 {
		t.Errorf("--env production was overridden by FUINGO_ENV")
	}
}

func TestMigrate(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fuingo.db")
	if _, err := runCommand(t, "--env", "development", "create", "--db", path); err != nil {
		t.Fatal(err)
	}
	if out, err := runCommand(t, "--env", "development", "migrate", "--db", path); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	if n := defaultPlayers(t, path); n != len(devdata.Players) {
		t.Errorf("migrate lost the default players")
	}

	missing := filepath.Join(dir, "missing.db")
	if _, err := runCommand(t, "--env", "development", "migrate", "--db", missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("migrate of a missing file = %v, want fs.ErrNotExist", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("migrate created %s", missing)
	}

	foreign := filepath.Join(dir, "foreign.db")
	if err := os.WriteFile(foreign, []byte(strings.Repeat("not sqlite\n", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(t, "--env", "development", "migrate", "--db", foreign); err == nil {
		t.Errorf("migrate of a non-database succeeded")
	}
}

func TestHelp(t *testing.T) {
	clearEnv(t)
	out, _ := runCommand(t, "--help")
	for _, want := range []string{"create", "migrate", "--db", "--env"} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not mention %s:\n%s", want, out)
		}
	}
}

func TestEnvironmentFiles(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(".env.development", []byte("FUINGO_DB=from-file.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCommand(t, "--env", "development", "create"); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if n := defaultPlayers(t, filepath.Join(dir, "from-file.db")); n != len(devdata.Players) {
		t.Errorf("%d default players, want %d", n, len(devdata.Players))
	}
}
