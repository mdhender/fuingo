// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"zombiezen.com/go/sqlite/sqlitex"
)

func seedUploaders(t *testing.T, s *Store) (game1, game2 int64) {
	t.Helper()
	err := s.CreateRows(t.Context(), Rows{
		Games: []string{"game01", "game02"},
		Uploaders: []NewUploader{
			{GameCode: "game01", Email: "a@example.com", CredentialHash: fixedHash(1)},
			{GameCode: "game01", Email: "b@example.com", CredentialHash: fixedHash(2)},
			{GameCode: "game02", Email: "c@example.com", CredentialHash: fixedHash(3)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return 1, 2
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestConcurrentRecordUpload runs identical uploads from several uploaders at
// once on a WAL database with a full pool.
func TestConcurrentRecordUpload(t *testing.T) {
	t.Parallel()
	s, err := Create(t.Context(), filepath.Join(t.TempDir(), "fuingo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	game1, game2 := seedUploaders(t, s)
	contents := []byte("setup orders\n")
	hash := sha(contents)

	uploaders := []struct {
		game  int64
		email string
	}{{game1, "a@example.com"}, {game1, "b@example.com"}, {game2, "c@example.com"}}
	const rounds = 8
	var wg sync.WaitGroup
	for range rounds {
		for _, u := range uploaders {
			wg.Go(func() {
				if err := s.RecordUpload(t.Context(), u.game, u.email, "1", hash, contents, time.Now()); err != nil {
					t.Errorf("RecordUpload: %v", err)
				}
			})
		}
	}
	wg.Wait()

	blobs, err := s.Blobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 || blobs[0].ContentHash != hash || string(blobs[0].Contents) != string(contents) {
		t.Errorf("blobs = %+v, want one", blobs)
	}
	uploads, err := s.Uploads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) != rounds*len(uploaders) {
		t.Errorf("%d uploads, want %d", len(uploads), rounds*len(uploaders))
	}
}

// TestRecordUploadIsAtomic checks that a failed row insert stores no blob.
func TestRecordUploadIsAtomic(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedUploaders(t, s)
	contents := []byte("orders\n")
	if err := s.RecordUpload(t.Context(), 1, "nobody@example.com", "1", sha(contents), contents, time.Now()); err == nil {
		t.Fatal("RecordUpload for an unknown uploader succeeded")
	}
	if blobs, _ := s.Blobs(t.Context()); len(blobs) != 0 {
		t.Errorf("blob stored without an upload row: %+v", blobs)
	}
}

// TestBlobsAreProtectedByForeignKeys checks that a blob cannot be deleted
// while any upload refers to it, that an upload cannot refer to a missing
// blob, and that unreferenced blobs can be swept.
func TestBlobsAreProtectedByForeignKeys(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	game1, game2 := seedUploaders(t, s)
	shared, other := []byte("shared\n"), []byte("other\n")
	for _, r := range []struct {
		game     int64
		email    string
		contents []byte
	}{{game1, "a@example.com", shared}, {game2, "c@example.com", shared}, {game1, "b@example.com", other}} {
		if err := s.RecordUpload(t.Context(), r.game, r.email, "1", sha(r.contents), r.contents, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	// The private pool has one connection; return it before calling s.Blobs.
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run := func(query string, args ...any) error {
		return sqlitex.Execute(conn, query, &sqlitex.ExecOptions{Args: args})
	}
	if err := run("DELETE FROM upload_blobs WHERE content_hash = ?;", sha(shared)); !isForeignKeyError(err) {
		t.Errorf("deleting a referenced blob = %v, want a foreign key error", err)
	}
	if err := run("INSERT INTO uploads (game_id, email, turn_no, content_hash, uploaded_at) VALUES (1, 'a@example.com', '1', ?, 'x');", sha([]byte("missing"))); !isForeignKeyError(err) {
		t.Errorf("upload of a missing blob = %v, want a foreign key error", err)
	}

	// Removing game01's history leaves "shared" referenced by game02 and
	// "other" orphaned; the sweep removes only the orphan.
	if err := run("DELETE FROM uploads WHERE game_id = ?;", game1); err != nil {
		t.Fatal(err)
	}
	if err := run("DELETE FROM upload_blobs WHERE content_hash NOT IN (SELECT content_hash FROM uploads);"); err != nil {
		t.Fatal(err)
	}
	s.pool.Put(conn)
	blobs, err := s.Blobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 || blobs[0].ContentHash != sha(shared) {
		t.Errorf("after the sweep, blobs = %+v, want only the shared one", blobs)
	}
}

// isForeignKeyError reports a foreign key violation. SQLite does not always
// set the extended result code (a RESTRICT action reports plain
// SQLITE_CONSTRAINT), so match the message.
func isForeignKeyError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// fixedHash returns a credential hash function that ignores the game.
func fixedHash(b byte) func(int64) []byte {
	return func(int64) []byte { return []byte{b} }
}

func TestGameCodesMustBeLowercase(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRows(t.Context(), Rows{Games: []string{"game01", "Game02"}}); !errors.Is(err, ErrGameCodeCase) {
		t.Errorf("CreateRows with an uppercase code = %v, want ErrGameCodeCase", err)
	}
	if games, _ := s.Games(t.Context()); len(games) != 0 {
		t.Errorf("games = %v, want none", games)
	}
}

func TestCreateGame(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.CreateGame(t.Context(), "game01")
	if err != nil {
		t.Fatalf("CreateGame: %v", err)
	}
	if got, ok, err := s.GameID(t.Context(), "game01"); err != nil || !ok || got != id {
		t.Errorf("GameID = %d, %v, %v; want %d, true, nil", got, ok, err, id)
	}
	if _, err := s.CreateGame(t.Context(), "game01"); !errors.Is(err, ErrGameExists) {
		t.Errorf("CreateGame with an existing code = %v, want ErrGameExists", err)
	}
	if _, err := s.CreateGame(t.Context(), "Game02"); !errors.Is(err, ErrGameCodeCase) {
		t.Errorf("CreateGame with an uppercase code = %v, want ErrGameCodeCase", err)
	}
	if games, _ := s.Games(t.Context()); len(games) != 1 {
		t.Errorf("games = %v, want only game01", games)
	}
}

func TestCreateAccount(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.CreateAccount(t.Context(), NewAccount{Email: "a@example.com", PasswordHash: []byte("hash-a")})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	a, ok, err := s.FindAccount(t.Context(), "a@example.com")
	if err != nil || !ok {
		t.Fatalf("FindAccount = %v, %v", ok, err)
	}
	if a.ID != id || a.Email != "a@example.com" || string(a.PasswordHash) != "hash-a" {
		t.Errorf("FindAccount = %+v, want ID %d and hash-a", a, id)
	}
	if _, err := s.CreateAccount(t.Context(), NewAccount{Email: "a@example.com", PasswordHash: []byte("hash-b")}); !errors.Is(err, ErrAccountExists) {
		t.Errorf("CreateAccount with an existing email = %v, want ErrAccountExists", err)
	}
	if a, _, _ := s.FindAccount(t.Context(), "a@example.com"); string(a.PasswordHash) != "hash-a" {
		t.Errorf("a duplicate CreateAccount changed the password hash to %q", a.PasswordHash)
	}
	if _, err := s.CreateAccount(t.Context(), NewAccount{Email: "B@example.com", PasswordHash: []byte("hash-b")}); !errors.Is(err, ErrEmailCase) {
		t.Errorf("CreateAccount with an uppercase email = %v, want ErrEmailCase", err)
	}
	if _, ok, _ := s.FindAccount(t.Context(), "missing@example.com"); ok {
		t.Error("FindAccount found a missing account")
	}
}

func TestCreateRowsIsAtomic(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.CreateRows(t.Context(), Rows{
		Games:    []string{"game01"},
		Accounts: []NewAccount{{Email: "a@example.com", PasswordHash: []byte("x")}, {Email: "a@example.com", PasswordHash: []byte("y")}},
	})
	if !errors.Is(err, ErrAccountExists) {
		t.Fatalf("CreateRows with a duplicate account = %v, want ErrAccountExists", err)
	}
	if games, _ := s.Games(t.Context()); len(games) != 0 {
		t.Errorf("games = %v, want none", games)
	}
	if _, ok, _ := s.FindAccount(t.Context(), "a@example.com"); ok {
		t.Error("CreateRows kept an account after failing")
	}
}

func TestAddManager(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRows(t.Context(), Rows{
		Games:    []string{"game01", "game02"},
		Accounts: []NewAccount{{Email: "a@example.com", PasswordHash: []byte("x")}, {Email: "b@example.com", PasswordHash: []byte("y")}},
		Managers: []NewManager{{GameCode: "game01", Email: "b@example.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddManager(t.Context(), NewManager{GameCode: "game01", Email: "a@example.com"}); err != nil {
		t.Fatalf("AddManager: %v", err)
	}
	if err := s.AddManager(t.Context(), NewManager{GameCode: "game02", Email: "a@example.com"}); err != nil {
		t.Fatalf("AddManager to a second game: %v", err)
	}
	for _, tc := range []struct {
		m    NewManager
		want error
	}{
		{NewManager{GameCode: "game01", Email: "a@example.com"}, ErrManagerExists},
		{NewManager{GameCode: "game99", Email: "a@example.com"}, ErrUnknownGame},
		{NewManager{GameCode: "game01", Email: "missing@example.com"}, ErrUnknownAccount},
	} {
		if err := s.AddManager(t.Context(), tc.m); !errors.Is(err, tc.want) {
			t.Errorf("AddManager(%+v) = %v, want %v", tc.m, err, tc.want)
		}
	}
	for game, want := range map[string][]string{
		"game01": {"a@example.com", "b@example.com"},
		"game02": {"a@example.com"},
	} {
		got, err := s.Managers(t.Context(), game)
		if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Managers(%s) = %v, %v; want %v", game, got, err, want)
		}
	}
}

func TestManagedRowsCannotBeDeleted(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRows(t.Context(), Rows{
		Games:    []string{"game01"},
		Accounts: []NewAccount{{Email: "a@example.com", PasswordHash: []byte("x")}},
		Managers: []NewManager{{GameCode: "game01", Email: "a@example.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	for _, stmt := range []string{"DELETE FROM games;", "DELETE FROM accounts;"} {
		if err := sqlitex.ExecuteTransient(conn, stmt, nil); !isForeignKeyError(err) {
			t.Errorf("%s = %v, want a foreign key error", stmt, err)
		}
	}
}

func TestSetManagerActive(t *testing.T) {
	t.Parallel()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRows(t.Context(), Rows{
		Games:    []string{"game01", "game02"},
		Accounts: []NewAccount{{Email: "a@example.com", PasswordHash: []byte("x")}, {Email: "b@example.com", PasswordHash: []byte("y")}},
		Managers: []NewManager{{GameCode: "game01", Email: "a@example.com"}, {GameCode: "game01", Email: "b@example.com"}, {GameCode: "game02", Email: "a@example.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	managers := func(game string) string {
		t.Helper()
		got, err := s.Managers(t.Context(), game)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, ",")
	}
	for range 2 { // disabling twice succeeds
		if err := s.SetManagerActive(t.Context(), "game01", "a@example.com", false); err != nil {
			t.Fatalf("SetManagerActive(false): %v", err)
		}
	}
	if got := managers("game01"); got != "b@example.com" {
		t.Errorf("managers of game01 = %s, want only b@example.com", got)
	}
	if got := managers("game02"); got != "a@example.com" {
		t.Errorf("managers of game02 = %s, want a@example.com", got)
	}
	if err := s.AddManager(t.Context(), NewManager{GameCode: "game01", Email: "a@example.com"}); !errors.Is(err, ErrManagerExists) {
		t.Errorf("AddManager of an inactive manager = %v, want ErrManagerExists", err)
	}
	if err := s.SetManagerActive(t.Context(), "game01", "a@example.com", true); err != nil {
		t.Fatalf("SetManagerActive(true): %v", err)
	}
	if got := managers("game01"); got != "a@example.com,b@example.com" {
		t.Errorf("managers of game01 = %s, want both", got)
	}
	for _, tc := range []struct{ game, email string }{
		{"game02", "b@example.com"},
		{"game99", "a@example.com"},
		{"game01", "missing@example.com"},
	} {
		if err := s.SetManagerActive(t.Context(), tc.game, tc.email, false); !errors.Is(err, ErrUnknownManager) {
			t.Errorf("SetManagerActive(%s, %s) = %v, want ErrUnknownManager", tc.game, tc.email, err)
		}
	}
}
