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
	err := s.CreateGamesAndUploaders(t.Context(), []string{"game01", "game02"}, []NewUploader{
		{GameCode: "game01", Email: "a@example.com", CredentialHash: fixedHash(1)},
		{GameCode: "game01", Email: "b@example.com", CredentialHash: fixedHash(2)},
		{GameCode: "game02", Email: "c@example.com", CredentialHash: fixedHash(3)},
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
	if err := s.CreateGamesAndUploaders(t.Context(), []string{"game01", "Game02"}, nil); !errors.Is(err, ErrGameCodeCase) {
		t.Errorf("CreateGamesAndUploaders with an uppercase code = %v, want ErrGameCodeCase", err)
	}
	if games, _ := s.Games(t.Context()); len(games) != 0 {
		t.Errorf("games = %v, want none", games)
	}
}
