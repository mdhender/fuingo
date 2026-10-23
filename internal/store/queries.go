// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

const (
	ErrGameExists     = cerrs.Error("game already exists")
	ErrUnknownGame    = cerrs.Error("unknown game")
	ErrUnknownUploader = cerrs.Error("unknown uploader")
	ErrUploaderExists = cerrs.Error("uploader already exists or credential is in use")
)

// timeLayout stores UTC timestamps at a fixed width so they sort as text.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// NewUploader is an upload account to insert. Email must already be
// lowercased, and CredentialHash computed with package credential.
type NewUploader struct {
	GameCode       string
	Email          string
	CredentialHash []byte
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
	err := sqlitex.Execute(conn, "INSERT INTO games (code, is_active) VALUES (?, 1);", &sqlitex.ExecOptions{
		Args: []any{code},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return fmt.Errorf("%s: %w", code, ErrGameExists)
	}
	return err
}

func createUploader(conn *sqlite.Conn, u NewUploader) error {
	err := sqlitex.Execute(conn, `
INSERT INTO game_uploaders (game_id, email, credential_hash, is_active)
SELECT id, ?, ?, 1 FROM games WHERE code = ?;`, &sqlitex.ExecOptions{
		Args: []any{u.Email, u.CredentialHash, u.GameCode},
	})
	switch sqlite.ErrCode(err) {
	case sqlite.ResultOK:
	case sqlite.ResultConstraintPrimaryKey, sqlite.ResultConstraintUnique:
		return ErrUploaderExists
	default:
		return err
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("%s: %w", u.GameCode, ErrUnknownGame)
	}
	return nil
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

// RecordUpload inserts a row in the upload history.
func (s *Store) RecordUpload(ctx context.Context, gameID int64, email, contentHash string, at time.Time) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn, "INSERT INTO uploads (game_id, email, content_hash, uploaded_at) VALUES (?, ?, ?, ?);", &sqlitex.ExecOptions{
		Args: []any{gameID, email, contentHash, at.UTC().Format(timeLayout)},
	})
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
SELECT u.id, u.game_id, g.code, u.email, u.content_hash, u.uploaded_at
FROM uploads u JOIN games g ON g.id = u.game_id
ORDER BY u.id;`, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			at, err := time.Parse(timeLayout, stmt.ColumnText(5))
			if err != nil {
				return err
			}
			uploads = append(uploads, Upload{
				ID:          stmt.ColumnInt64(0),
				GameID:      stmt.ColumnInt64(1),
				GameCode:    stmt.ColumnText(2),
				Email:       stmt.ColumnText(3),
				ContentHash: stmt.ColumnText(4),
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
