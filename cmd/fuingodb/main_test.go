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

	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/store"
)

const pepper = "test-pepper"

// clearEnv keeps the developer's environment out of the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FUINGO_ENV", "FUINGO_DB", "FUINGO_PEPPER"} {
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

// defaultUploaders reports how many default upload accounts the database
// accepts under pepper.
func defaultUploaders(t *testing.T, path string) int {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	n := 0
	for _, u := range devdata.Uploaders {
		gameID, ok, err := s.GameID(t.Context(), u.Game)
		if err != nil {
			t.Fatal(err)
		} else if !ok {
			continue
		}
		if _, ok, err := s.FindUploader(t.Context(), credential.Hash(pepper, u.Email, gameID, u.Token)); err != nil {
			t.Fatal(err)
		} else if ok {
			n++
		}
	}
	return n
}

func TestCreateSeedsDefaultsInDevelopmentAndAgent(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	for _, env := range []string{"development", "agent"} {
		path := filepath.Join(t.TempDir(), "fuingo.db")
		if out, err := runCommand(t, "--env", env, "create", "--db", path); err != nil {
			t.Fatalf("%s: create: %v\n%s", env, err, out)
		}
		if n := defaultUploaders(t, path); n != len(devdata.Uploaders) {
			t.Errorf("%s: %d default uploaders, want %d", env, n, len(devdata.Uploaders))
		}
	}
}

func TestCreateDoesNotSeedOtherEnvironments(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	for _, env := range []string{"test", "production"} {
		path := filepath.Join(t.TempDir(), "fuingo.db")
		if out, err := runCommand(t, "--env", env, "create", "--db", path); err != nil {
			t.Fatalf("%s: create: %v\n%s", env, err, out)
		}
		if n := defaultUploaders(t, path); n != 0 {
			t.Errorf("%s: %d default uploaders, want 0", env, n)
		}
	}
}

func TestCreateRequiresPepper(t *testing.T) {
	clearEnv(t)
	for _, env := range []string{"development", "production"} {
		path := filepath.Join(t.TempDir(), "fuingo.db")
		if _, err := runCommand(t, "--env", env, "create", "--db", path); !errors.Is(err, cli.ErrMissingPepper) {
			t.Errorf("%s: create = %v, want ErrMissingPepper", env, err)
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: create made %s without a pepper", env, path)
		}
	}
}

func TestCreateRefusesExistingFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
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
	t.Setenv("FUINGO_PEPPER", pepper)
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
	t.Setenv("FUINGO_PEPPER", pepper)
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
	t.Setenv("FUINGO_PEPPER", pepper)
	dir := t.TempDir()
	fromEnv := filepath.Join(dir, "env.db")
	fromFlag := filepath.Join(dir, "flag.db")
	t.Setenv("FUINGO_ENV", "development")
	t.Setenv("FUINGO_DB", fromEnv)

	if out, err := runCommand(t, "create"); err != nil {
		t.Fatalf("create from environment: %v\n%s", err, out)
	}
	if n := defaultUploaders(t, fromEnv); n != len(devdata.Uploaders) {
		t.Errorf("FUINGO_ENV=development did not seed defaults")
	}

	// Explicit flags override the environment.
	if out, err := runCommand(t, "--env", "production", "create", "--db", fromFlag); err != nil {
		t.Fatalf("create with flags: %v\n%s", err, out)
	}
	if n := defaultUploaders(t, fromFlag); n != 0 {
		t.Errorf("--env production was overridden by FUINGO_ENV")
	}
}

func TestMigrate(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	dir := t.TempDir()
	path := filepath.Join(dir, "fuingo.db")
	if _, err := runCommand(t, "--env", "development", "create", "--db", path); err != nil {
		t.Fatal(err)
	}
	if out, err := runCommand(t, "--env", "development", "migrate", "--db", path); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	if n := defaultUploaders(t, path); n != len(devdata.Uploaders) {
		t.Errorf("migrate lost the default uploaders")
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
	// The pepper belongs in an ignored .local file; shared files never hold it.
	if err := os.WriteFile(".env.development.local", []byte("FUINGO_PEPPER="+pepper+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env.development", []byte("FUINGO_DB=from-file.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCommand(t, "--env", "development", "create"); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if n := defaultUploaders(t, filepath.Join(dir, "from-file.db")); n != len(devdata.Uploaders) {
		t.Errorf("%d default uploaders, want %d", n, len(devdata.Uploaders))
	}
}
