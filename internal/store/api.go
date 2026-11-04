// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// CreateSession stores an API session for the account, keyed by the SHA-256
// of its bearer token, and deletes every session that has expired by now.
func (s *Store) CreateSession(ctx context.Context, accountID int64, hashedToken []byte, now, expiresAt time.Time) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	if err := sqlitex.Execute(conn, "DELETE FROM api_sessions WHERE expires_at <= ?;", &sqlitex.ExecOptions{
		Args: []any{now.UTC().Format(timeLayout)},
	}); err != nil {
		return err
	}
	return sqlitex.Execute(conn, "INSERT INTO api_sessions (account_id, hashed_token, created_at, expires_at) VALUES (?, ?, ?, ?);", &sqlitex.ExecOptions{
		Args: []any{accountID, hashedToken, now.UTC().Format(timeLayout), expiresAt.UTC().Format(timeLayout)},
	})
}

// SessionAccount returns the account of the API session with the given
// token hash. It reports false if there is none, if it has expired by now,
// or if its account is inactive or has no hashed secret.
func (s *Store) SessionAccount(ctx context.Context, hashedToken []byte, now time.Time) (Account, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Account{}, false, err
	}
	defer s.pool.Put(conn)
	var a Account
	var found bool
	err = sqlitex.Execute(conn, `
SELECT a.id, a.email, a.hashed_secret
FROM api_sessions s JOIN accounts a ON a.id = s.account_id
WHERE s.hashed_token = ? AND s.expires_at > ?
  AND a.is_active = 1 AND a.hashed_secret IS NOT NULL;`, &sqlitex.ExecOptions{
		Args: []any{hashedToken, now.UTC().Format(timeLayout)},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			a = Account{ID: stmt.ColumnInt64(0), Email: stmt.ColumnText(1), HashedSecret: []byte(stmt.ColumnText(2)), Active: true}
			found = true
			return nil
		},
	})
	return a, found, err
}

// ManagedGameID returns the ID of the game with the given lowercased code
// if the account actively manages it. The game may be active or not.
func (s *Store) ManagedGameID(ctx context.Context, accountID int64, code string) (int64, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, false, err
	}
	defer s.pool.Put(conn)
	var id int64
	var found bool
	err = sqlitex.Execute(conn, `
SELECT g.id FROM games g JOIN active_managers m ON m.game_id = g.id
WHERE g.code = ? AND m.account_id = ?;`, &sqlitex.ExecOptions{
		Args: []any{code, accountID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			id, found = stmt.ColumnInt64(0), true
			return nil
		},
	})
	return id, found, err
}

// UploadInfo describes one upload without its contents.
type UploadInfo struct {
	ID          int64
	Email       string
	TurnNo      string
	ContentHash string
	Size        int64
	UploadedAt  time.Time
}

// GameUploads returns the uploads to the game, oldest first. If turn is not
// nil, it returns only the uploads whose turn number is exactly *turn.
func (s *Store) GameUploads(ctx context.Context, gameID int64, turn *string) ([]UploadInfo, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var turnArg any // NULL matches every turn
	if turn != nil {
		turnArg = *turn
	}
	uploads := []UploadInfo{}
	err = sqlitex.Execute(conn, `
SELECT u.id, u.email, u.turn_no, u.content_hash, length(b.contents), u.uploaded_at
FROM uploads u
JOIN game_uploaders gu ON gu.id = u.uploader_id
JOIN upload_blobs b ON b.content_hash = u.content_hash
WHERE gu.game_id = ?1 AND (?2 IS NULL OR u.turn_no = ?2)
ORDER BY u.id;`, &sqlitex.ExecOptions{
		Args: []any{gameID, turnArg},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			at, err := time.Parse(timeLayout, stmt.ColumnText(5))
			if err != nil {
				return err
			}
			uploads = append(uploads, UploadInfo{
				ID:          stmt.ColumnInt64(0),
				Email:       stmt.ColumnText(1),
				TurnNo:      stmt.ColumnText(2),
				ContentHash: stmt.ColumnText(3),
				Size:        stmt.ColumnInt64(4),
				UploadedAt:  at,
			})
			return nil
		},
	})
	return uploads, err
}

// GameUploadContents returns the content hash and contents of an upload,
// reporting false unless the upload was made to the game.
func (s *Store) GameUploadContents(ctx context.Context, gameID, uploadID int64) (string, []byte, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return "", nil, false, err
	}
	defer s.pool.Put(conn)
	var hash string
	var contents []byte
	var found bool
	err = sqlitex.Execute(conn, `
SELECT b.content_hash, b.contents
FROM uploads u
JOIN game_uploaders gu ON gu.id = u.uploader_id
JOIN upload_blobs b ON b.content_hash = u.content_hash
WHERE u.id = ? AND gu.game_id = ?;`, &sqlitex.ExecOptions{
		Args: []any{uploadID, gameID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			hash = stmt.ColumnText(0)
			contents = make([]byte, stmt.ColumnLen(1))
			stmt.ColumnBytes(1, contents)
			found = true
			return nil
		},
	})
	return hash, contents, found, err
}
