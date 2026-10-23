// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

const (
	ErrGameExists      = cerrs.Error("game already exists")
	ErrGameCodeCase    = cerrs.Error("game code must be lowercase")
	ErrUnknownGame     = cerrs.Error("unknown game")
	ErrUnknownUploader = cerrs.Error("unknown uploader")
	ErrUploaderExists  = cerrs.Error("uploader already exists or credential is in use")
)

// timeLayout stores UTC timestamps at a fixed width so they sort as text.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// NewUploader is an upload account to insert. GameCode and Email must
// already be lowercased. CredentialHash computes the credential hash (with package
// credential) from the game's ID, which is looked up in the same transaction
// as the insert.
type NewUploader struct {
	GameCode       string
	Email          string
	CredentialHash func(gameID int64) []byte
}

// Uploader is an active upload account in an active game.
type Uploader struct {
	GameID         int64
	Email          string
	CredentialHash []byte
}

// Upload is one row of the upload history.
type Upload struct {
	ID          int64
	GameID      int64
	GameCode    string
	Email       string
	TurnNo      string
	ContentHash string
	UploadedAt  time.Time
}

// CreateGamesAndUploaders inserts active games and upload accounts in one
// transaction. It fails, inserting nothing, if any game or uploader exists.
func (s *Store) CreateGamesAndUploaders(ctx context.Context, games []string, uploaders []NewUploader) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	for _, code := range games {
		if err := createGame(conn, code); err != nil {
			return err
		}
	}
	for _, u := range uploaders {
		if err := createUploader(conn, u); err != nil {
			return err
		}
	}
	return nil
}

// CreateUploader inserts an active upload account for an existing game.
// It never modifies an existing row: a duplicate game and email, or a
// credential hash already in use, fails with ErrUploaderExists.
func (s *Store) CreateUploader(ctx context.Context, u NewUploader) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return createUploader(conn, u)
}

func createGame(conn *sqlite.Conn, code string) error {
	if code != strings.ToLower(code) {
		return fmt.Errorf("%s: %w", code, ErrGameCodeCase)
	}
	err := sqlitex.Execute(conn, "INSERT INTO games (code, is_active) VALUES (?, 1);", &sqlitex.ExecOptions{
		Args: []any{code},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return fmt.Errorf("%s: %w", code, ErrGameExists)
	}
	return err
}

func createUploader(conn *sqlite.Conn, u NewUploader) (err error) {
	defer sqlitex.Save(conn)(&err)
	gameID, ok, err := gameID(conn, u.GameCode)
	if err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s: %w", u.GameCode, ErrUnknownGame)
	}
	err = sqlitex.Execute(conn, "INSERT INTO game_uploaders (game_id, email, credential_hash, is_active) VALUES (?, ?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{gameID, u.Email, u.CredentialHash(gameID)},
	})
	switch sqlite.ErrCode(err) {
	case sqlite.ResultConstraintPrimaryKey, sqlite.ResultConstraintUnique:
		return ErrUploaderExists
	}
	return err
}

// GameID returns the ID of the game with the given code, active or not.
func (s *Store) GameID(ctx context.Context, code string) (int64, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, false, err
	}
	defer s.pool.Put(conn)
	return gameID(conn, code)
}

func gameID(conn *sqlite.Conn, code string) (int64, bool, error) {
	var id int64
	var found bool
	err := sqlitex.Execute(conn, "SELECT id FROM games WHERE code = ?;", &sqlitex.ExecOptions{
		Args: []any{code},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			id, found = stmt.ColumnInt64(0), true
			return nil
		},
	})
	return id, found, err
}

// FindUploader returns the upload account with the given credential hash.
// It reports false if there is none, or if the account or its game is
// inactive.
func (s *Store) FindUploader(ctx context.Context, credentialHash []byte) (Uploader, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Uploader{}, false, err
	}
	defer s.pool.Put(conn)
	var u Uploader
	var found bool
	err = sqlitex.Execute(conn, `
SELECT u.game_id, u.email, u.credential_hash
FROM game_uploaders u
JOIN games g ON g.id = u.game_id
WHERE u.credential_hash = ? AND u.is_active = 1 AND g.is_active = 1;`, &sqlitex.ExecOptions{
		Args: []any{credentialHash},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			found = true
			u.GameID = stmt.ColumnInt64(0)
			u.Email = stmt.ColumnText(1)
			u.CredentialHash = make([]byte, stmt.ColumnLen(2))
			stmt.ColumnBytes(2, u.CredentialHash)
			return nil
		},
	})
	if err != nil {
		return Uploader{}, false, err
	}
	return u, found, nil
}

// RecordUpload stores contents under contentHash and inserts a row in the
// upload history for the given turn, in one transaction. Contents already stored under that hash
// are kept as they are, so every upload of identical bytes shares one blob.
func (s *Store) RecordUpload(ctx context.Context, gameID int64, email, turnNo, contentHash string, contents []byte, at time.Time) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	end, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return err
	}
	defer end(&err)
	uploadedAt := at.UTC().Format(timeLayout)
	if err := sqlitex.Execute(conn, `
INSERT INTO upload_blobs (content_hash, contents, created_at) VALUES (?, ?, ?)
ON CONFLICT (content_hash) DO NOTHING;`, &sqlitex.ExecOptions{
		Args: []any{contentHash, contents, uploadedAt},
	}); err != nil {
		return err
	}
	return sqlitex.Execute(conn, "INSERT INTO uploads (game_id, email, turn_no, content_hash, uploaded_at) VALUES (?, ?, ?, ?, ?);", &sqlitex.ExecOptions{
		Args: []any{gameID, email, turnNo, contentHash, uploadedAt},
	})
}

// Blob is stored upload content.
type Blob struct {
	ContentHash string
	Contents    []byte
	CreatedAt   time.Time
}

// Blobs returns every stored blob, in hash order.
func (s *Store) Blobs(ctx context.Context) ([]Blob, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var blobs []Blob
	err = sqlitex.Execute(conn, "SELECT content_hash, contents, created_at FROM upload_blobs ORDER BY content_hash;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			at, err := time.Parse(timeLayout, stmt.ColumnText(2))
			if err != nil {
				return err
			}
			contents := make([]byte, stmt.ColumnLen(1))
			stmt.ColumnBytes(1, contents)
			blobs = append(blobs, Blob{ContentHash: stmt.ColumnText(0), Contents: contents, CreatedAt: at})
			return nil
		},
	})
	return blobs, err
}

// Uploads returns the upload history, oldest first.
func (s *Store) Uploads(ctx context.Context) ([]Upload, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var uploads []Upload
	err = sqlitex.Execute(conn, `
SELECT u.id, u.game_id, g.code, u.email, u.turn_no, u.content_hash, u.uploaded_at
FROM uploads u JOIN games g ON g.id = u.game_id
ORDER BY u.id;`, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			at, err := time.Parse(timeLayout, stmt.ColumnText(6))
			if err != nil {
				return err
			}
			uploads = append(uploads, Upload{
				ID:          stmt.ColumnInt64(0),
				GameID:      stmt.ColumnInt64(1),
				GameCode:    stmt.ColumnText(2),
				Email:       stmt.ColumnText(3),
				TurnNo:      stmt.ColumnText(4),
				ContentHash: stmt.ColumnText(5),
				UploadedAt:  at,
			})
			return nil
		},
	})
	return uploads, err
}

// Games returns the codes of every game, in ID order.
func (s *Store) Games(ctx context.Context) ([]string, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var codes []string
	err = sqlitex.Execute(conn, "SELECT code FROM games ORDER BY id;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			codes = append(codes, stmt.ColumnText(0))
			return nil
		},
	})
	return codes, err
}

// SetGameActive sets whether a game accepts uploads.
func (s *Store) SetGameActive(ctx context.Context, code string, active bool) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	if err := sqlitex.Execute(conn, "UPDATE games SET is_active = ? WHERE code = ?;", &sqlitex.ExecOptions{
		Args: []any{active, code},
	}); err != nil {
		return err
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("%s: %w", code, ErrUnknownGame)
	}
	return nil
}

// SetUploaderActive sets whether an upload account may upload.
func (s *Store) SetUploaderActive(ctx context.Context, gameCode, email string, active bool) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	if err := sqlitex.Execute(conn, `
UPDATE game_uploaders SET is_active = ?
WHERE email = ? AND game_id = (SELECT id FROM games WHERE code = ?);`, &sqlitex.ExecOptions{
		Args: []any{active, email, gameCode},
	}); err != nil {
		return err
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("%s %s: %w", gameCode, email, ErrUnknownUploader)
	}
	return nil
}
