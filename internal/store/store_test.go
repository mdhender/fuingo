// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func TestCreateOpenMigrate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fuingo.db")
	s, err := Create(t.Context(), path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := Migrate(t.Context(), path); err != nil {
		t.Fatalf("Migrate on current schema: %v", err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	checkPoolInvariants(t, s, persistentPoolSize, "wal")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestCreateRefusesExistingTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := map[string][]byte{
		"empty.db":   nil,
		"text.db":    []byte("not a database\n"),
		"fuingo.db":  nil, // replaced below with a real fuingo database
		"foreign.db": nil, // replaced below with a foreign database
	}
	for name, data := range cases {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate(t, filepath.Join(dir, "fuingo2.db"))
	copyFile(t, filepath.Join(dir, "fuingo2.db"), filepath.Join(dir, "fuingo.db"))
	writeForeign(t, filepath.Join(dir, "foreign.db"), 0x12345678, 0)

	for name := range cases {
		path := filepath.Join(dir, name)
		before := snapshot(t, dir)
		_, err := Create(t.Context(), path)
		if !errors.Is(err, ErrDatabaseExists) {
			t.Errorf("Create(%s) = %v, want ErrDatabaseExists", name, err)
		}
		if after := snapshot(t, dir); !equalSnapshots(before, after) {
			t.Errorf("Create(%s) modified the directory", name)
		}
	}
}

func TestCreateRefusesLeftoverJournal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "fuingo.db")
	if err := os.WriteFile(path+"-wal", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(t.Context(), path); !errors.Is(err, ErrDatabaseExists) {
		t.Fatalf("Create = %v, want ErrDatabaseExists", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Create left %s behind: %v", path, err)
	}
	if data, _ := os.ReadFile(path + "-wal"); string(data) != "stale" {
		t.Fatalf("Create changed the leftover journal")
	}
}

func TestMissingParentDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "fuingo.db")
	if _, err := Create(t.Context(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Create = %v, want fs.ErrNotExist", err)
	}
	if _, err := Open(t.Context(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open = %v, want fs.ErrNotExist", err)
	}
	if err := Migrate(t.Context(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Migrate = %v, want fs.ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("parent directory was created")
	}
}

func TestOpenAndMigrateRefuseMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "fuingo.db")
	if _, err := Open(t.Context(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open = %v, want fs.ErrNotExist", err)
	}
	if err := Migrate(t.Context(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Migrate = %v, want fs.ErrNotExist", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("directory is not empty: %v", entries)
	}
}

func TestOpenAndMigrateRefuseUnownedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "text.db"), bytes.Repeat([]byte("not a database\n"), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	writeForeign(t, filepath.Join(dir, "foreign.db"), 0x12345678, 1)
	writeForeign(t, filepath.Join(dir, "zero.db"), 0, 0)
	writeForeign(t, filepath.Join(dir, "zero-with-version.db"), 0, 1)

	for _, name := range []string{"empty.db", "text.db", "foreign.db", "zero.db", "zero-with-version.db"} {
		path := filepath.Join(dir, name)
		before := snapshot(t, dir)
		if s, err := Open(t.Context(), path); err == nil {
			s.Close()
			t.Errorf("Open(%s) succeeded", name)
		}
		if err := Migrate(t.Context(), path); err == nil {
			t.Errorf("Migrate(%s) succeeded", name)
		}
		if after := snapshot(t, dir); !equalSnapshots(before, after) {
			t.Errorf("Open or Migrate of %s modified the directory", name)
		}
	}
	// The ownership errors are reported as such.
	for _, name := range []string{"empty.db", "foreign.db", "zero.db"} {
		if _, err := Open(t.Context(), filepath.Join(dir, name)); !errors.Is(err, ErrNotOwned) {
			t.Errorf("Open(%s) = %v, want ErrNotOwned", name, err)
		}
	}
}

func TestOpenRefusesOutdatedSchemaAndMigrateUpdatesIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.db")
	writeForeign(t, path, applicationID, 0)
	if _, err := Open(t.Context(), path); !errors.Is(err, ErrOutdatedSchema) {
		t.Fatalf("Open = %v, want ErrOutdatedSchema", err)
	}
	if v := readUserVersion(t, path); v != 0 {
		t.Fatalf("Open changed the schema version to %d", v)
	}
	if err := Migrate(t.Context(), path); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open after Migrate: %v", err)
	}
	checkPoolInvariants(t, s, persistentPoolSize, "wal")
	s.Close()
}

func TestNewerSchemaIsRefused(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "new.db")
	writeForeign(t, path, applicationID, len(migrations)+1)
	if _, err := Open(t.Context(), path); !errors.Is(err, ErrNewerSchema) {
		t.Errorf("Open = %v, want ErrNewerSchema", err)
	}
	if err := Migrate(t.Context(), path); !errors.Is(err, ErrNewerSchema) {
		t.Errorf("Migrate = %v, want ErrNewerSchema", err)
	}
	if v := readUserVersion(t, path); v != len(migrations)+1 {
		t.Errorf("schema version changed to %d", v)
	}
}

func TestOpenRefusesNonRegularFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, path := range []string{dir, "", ":memory:", "file:x.db?mode=rwc"} {
		if _, err := Open(t.Context(), path); err == nil {
			t.Errorf("Open(%q) succeeded", path)
		}
	}
}

func TestPrivateMemoryIsIsolated(t *testing.T) {
	t.Parallel()
	a, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatalf("OpenPrivateMemory: %v", err)
	}
	defer a.Close()
	b, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatalf("OpenPrivateMemory: %v", err)
	}
	defer b.Close()
	checkPoolInvariants(t, a, 1, "memory")
	exec(t, a, "INSERT INTO games (code) VALUES ('game01');")
	if n := countGames(t, a); n != 1 {
		t.Errorf("a has %d games, want 1", n)
	}
	if n := countGames(t, b); n != 0 {
		t.Errorf("b has %d games, want 0", n)
	}
}

func TestSharedMemory(t *testing.T) {
	t.Parallel()
	name := t.Name()
	a, err := CreateSharedMemory(t.Context(), name)
	if err != nil {
		t.Fatalf("CreateSharedMemory: %v", err)
	}
	if _, err := CreateSharedMemory(t.Context(), name); !errors.Is(err, ErrDatabaseExists) {
		t.Fatalf("second CreateSharedMemory = %v, want ErrDatabaseExists", err)
	}
	b, err := OpenSharedMemory(t.Context(), name)
	if err != nil {
		t.Fatalf("OpenSharedMemory: %v", err)
	}
	checkPoolInvariants(t, b, 3, "memory")
	exec(t, a, "INSERT INTO games (code) VALUES ('game01');")
	if n := countGames(t, b); n != 1 {
		t.Errorf("b has %d games, want 1", n)
	}

	// The database outlives a's pool while b still holds it.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if n := countGames(t, b); n != 1 {
		t.Errorf("after closing a, b has %d games, want 1", n)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	// With every connection closed, the database is gone.
	if _, err := OpenSharedMemory(t.Context(), name); !errors.Is(err, ErrNotOwned) {
		t.Errorf("OpenSharedMemory after close = %v, want ErrNotOwned", err)
	}
}

func TestSharedMemoryRefusesUnownedDatabase(t *testing.T) {
	t.Parallel()
	uri, _ := memoryURI(t.Name(), "shared")
	conn, err := sqlite.OpenConn(uri, sqlite.OpenReadWrite|sqlite.OpenCreate|sqlite.OpenURI)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := sqlitex.ExecuteScript(conn, "CREATE TABLE t (x);", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateSharedMemory(t.Context(), t.Name()); !errors.Is(err, ErrDatabaseExists) {
		t.Errorf("CreateSharedMemory = %v, want ErrDatabaseExists", err)
	}
	if _, err := OpenSharedMemory(t.Context(), t.Name()); !errors.Is(err, ErrNotOwned) {
		t.Errorf("OpenSharedMemory = %v, want ErrNotOwned", err)
	}
	if id, _ := readApplicationID(conn); id != 0 {
		t.Errorf("application ID was stamped: %#x", id)
	}
}

func TestParallelPrivateMemoryIsolation(t *testing.T) {
	for i := range 8 {
		t.Run(strings.Repeat("x", i+1), func(t *testing.T) {
			t.Parallel()
			s, err := OpenPrivateMemory(t.Context(), t.Name())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for range i + 1 {
				exec(t, s, "INSERT INTO games (code) VALUES (hex(randomblob(8)));")
			}
			if n := countGames(t, s); n != i+1 {
				t.Errorf("%d games, want %d", n, i+1)
			}
		})
	}
}

// checkPoolInvariants borrows n connections at once and checks that each
// enforces the journal mode and foreign keys.
func checkPoolInvariants(t *testing.T, s *Store, n int, journalMode string) {
	t.Helper()
	var conns []*sqlite.Conn
	defer func() {
		for _, conn := range conns {
			s.pool.Put(conn)
		}
	}()
	for range n {
		conn, err := s.take(t.Context())
		if err != nil {
			t.Fatalf("take: %v", err)
		}
		conns = append(conns, conn)
	}
	for i, conn := range conns {
		if mode, err := pragmaText(conn, "PRAGMA journal_mode;"); err != nil || mode != journalMode {
			t.Errorf("conn %d: journal_mode = %q, %v; want %q", i, mode, err, journalMode)
		}
		if fk, err := pragmaText(conn, "PRAGMA foreign_keys;"); err != nil || fk != "1" {
			t.Errorf("conn %d: foreign_keys = %q, %v; want 1", i, fk, err)
		}
		err := sqlitex.ExecuteTransient(conn, "INSERT INTO game_uploaders (game_id, account_id, hashed_credentials) VALUES (-1, -1, x'00');", nil)
		if sqlite.ErrCode(err) != sqlite.ResultConstraintForeignKey {
			t.Errorf("conn %d: insert with unknown game = %v, want foreign key error", i, err)
		}
	}
}

func exec(t *testing.T, s *Store, query string) {
	t.Helper()
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	if err := sqlitex.ExecuteTransient(conn, query, nil); err != nil {
		t.Fatal(err)
	}
}

func countGames(t *testing.T, s *Store) int {
	t.Helper()
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	n, err := sqlitex.ResultInt(conn.Prep("SELECT count(*) FROM games;"))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func mustCreate(t *testing.T, path string) {
	t.Helper()
	s, err := Create(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeForeign makes a SQLite database with the given application ID and
// schema version, and one table, without using this package.
func writeForeign(t *testing.T, path string, id int32, version int) {
	t.Helper()
	conn, err := sqlite.OpenConn(path, sqlite.OpenReadWrite|sqlite.OpenCreate)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	script := "CREATE TABLE other (x);\n"
	if id != 0 {
		script += "PRAGMA application_id = " + strconv.Itoa(int(id)) + ";\n"
	}
	script += "PRAGMA user_version = " + strconv.Itoa(version) + ";\n"
	if err := sqlitex.ExecuteScript(conn, script, nil); err != nil {
		t.Fatal(err)
	}
}

func readUserVersion(t *testing.T, path string) int {
	t.Helper()
	conn, err := sqlite.OpenConn(path, sqlite.OpenReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	v, err := userVersion(conn)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshot records the name and contents of every file in dir.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(data)
	}
	return files
}

func equalSnapshots(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || v != w {
			return false
		}
	}
	return true
}

func TestSchemaVersion(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fuingo.db")
	mustCreate(t, path)
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	got, err := s.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if want := CurrentSchemaVersion(); got != want || want != len(migrations) {
		t.Errorf("SchemaVersion = %d, CurrentSchemaVersion = %d, want %d", got, want, len(migrations))
	}
}
