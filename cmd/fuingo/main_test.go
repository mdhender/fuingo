// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/store"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// clearEnv keeps the developer's environment out of the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FUINGO_ENV", "FUINGO_DB"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func runCommand(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = run(t.Context(), args, &out, &errOut)
	if err != nil {
		return out.String() + errOut.String(), err
	}
	return out.String(), nil
}

// createDatabase makes a current database and returns its path.
func createDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fuingo.db")
	s, err := store.Create(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// setSchemaVersion overwrites the database's schema version.
func setSchemaVersion(t *testing.T, path string, version int) {
	t.Helper()
	conn, err := sqlite.OpenConn(path, sqlite.OpenReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := sqlitex.ExecuteTransient(conn, "PRAGMA user_version = "+strconv.Itoa(version)+";", nil); err != nil {
		t.Fatal(err)
	}
}

func TestVersion(t *testing.T) {
	clearEnv(t)
	out, err := runCommand(t, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	if want := fuingo.Version().String() + "\n"; out != want {
		t.Errorf("version printed %q, want %q", out, want)
	}
}

func TestDBVersion(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	want := strconv.Itoa(store.CurrentSchemaVersion()) + "\n"
	for _, args := range [][]string{
		{"--env", "test", "db", "--db", path, "version"},
		{"--env", "test", "db", "version", "--db", path},
	} {
		out, err := runCommand(t, args...)
		if err != nil {
			t.Fatalf("%q: %v\n%s", args, err, out)
		}
		if out != want {
			t.Errorf("%q printed %q, want %q", args, out, want)
		}
	}
}

func TestDBVersionFromEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_ENV", "test")
	t.Setenv("FUINGO_DB", createDatabase(t))
	out, err := runCommand(t, "db", "version")
	if err != nil {
		t.Fatalf("db version: %v\n%s", err, out)
	}
	if want := strconv.Itoa(store.CurrentSchemaVersion()) + "\n"; out != want {
		t.Errorf("db version printed %q, want %q", out, want)
	}
}

func TestDBVersionRefusesOtherSchemaVersions(t *testing.T) {
	clearEnv(t)
	for _, tc := range []struct {
		version int
		want    error
	}{
		{store.CurrentSchemaVersion() - 1, store.ErrOutdatedSchema},
		{store.CurrentSchemaVersion() + 1, store.ErrNewerSchema},
	} {
		path := createDatabase(t)
		setSchemaVersion(t, path, tc.version)
		out, err := runCommand(t, "--env", "test", "db", "--db", path, "version")
		if !errors.Is(err, tc.want) {
			t.Errorf("schema version %d: err = %v, want %v\n%s", tc.version, err, tc.want, out)
		}
	}
}

func TestDBVersionRefusesBadPaths(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	foreign := filepath.Join(dir, "foreign.db")
	if err := os.WriteFile(foreign, []byte("not a database\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.db")
	for _, tc := range []struct {
		path string
		want error
	}{
		{"", ErrMissingDatabase},
		{":memory:", ErrMemoryDatabase},
		{missing, fs.ErrNotExist},
	} {
		out, err := runCommand(t, "--env", "test", "db", "--db", tc.path, "version")
		if !errors.Is(err, tc.want) {
			t.Errorf("--db %q: err = %v, want %v\n%s", tc.path, err, tc.want, out)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("db version created %s", missing)
	}
	if out, err := runCommand(t, "--env", "test", "db", "--db", foreign, "version"); err == nil {
		t.Errorf("db version accepted a file that is not a fuingo database:\n%s", out)
	}
}

func TestDBRequiresEnvironment(t *testing.T) {
	clearEnv(t)
	out, err := runCommand(t, "db", "--db", createDatabase(t), "version")
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Errorf("db version without an environment: err = %v\n%s", err, out)
	}
}
