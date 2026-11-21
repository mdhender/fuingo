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

// CreateManagedGame inserts an active game and makes the account its active
// manager, in one transaction. The code must be lowercase. A duplicate code
// fails with ErrGameExists.
func (s *Store) CreateManagedGame(ctx context.Context, code string, accountID int64) (id int64, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	if id, err = createGame(conn, code); err != nil {
		return 0, err
	}
	err = sqlitex.Execute(conn, "INSERT INTO game_managers (game_id, account_id, is_active) VALUES (?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{id, accountID},
	})
	return id, err
}

// UploaderInfo describes one upload credential of a game. Active is the
// credential's own flag; AccountActive is its account's. Both, and the
// game, must be active for it to upload.
type UploaderInfo struct {
	ID            int64
	Email         string
	Active        bool
	AccountActive bool
}

// GameUploaders returns the game's upload credentials, active or not, in
// order of ID.
func (s *Store) GameUploaders(ctx context.Context, gameID int64) ([]UploaderInfo, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var uploaders []UploaderInfo
	err = sqlitex.Execute(conn, `
SELECT gu.id, a.email, gu.is_active, a.is_active
FROM game_uploaders gu JOIN accounts a ON a.id = gu.account_id
WHERE gu.game_id = ?
ORDER BY gu.id;`, &sqlitex.ExecOptions{
		Args: []any{gameID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			uploaders = append(uploaders, UploaderInfo{
				ID:            stmt.ColumnInt64(0),
				Email:         stmt.ColumnText(1),
				Active:        stmt.ColumnBool(2),
				AccountActive: stmt.ColumnBool(3),
			})
			return nil
		},
	})
	return uploaders, err
}

// AddGameUploader inserts an active upload credential for the game and
// returns its ID, creating the account, without a hashed secret, if the
// lowercased email has none. A duplicate game and email, or a credential
// hash already in use, fails with ErrUploaderExists.
func (s *Store) AddGameUploader(ctx context.Context, gameID int64, email string, hashedCredentials []byte) (id int64, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	account, ok, err := accountID(conn, email)
	if err != nil {
		return 0, err
	} else if !ok {
		if account, err = createAccount(conn, NewAccount{Email: email}); err != nil {
			return 0, err
		}
	}
	err = sqlitex.Execute(conn, "INSERT INTO game_uploaders (game_id, account_id, hashed_credentials, is_active) VALUES (?, ?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{gameID, account, hashedCredentials},
	})
	switch sqlite.ErrCode(err) {
	case sqlite.ResultConstraintPrimaryKey, sqlite.ResultConstraintUnique:
		return 0, ErrUploaderExists
	}
	if err != nil {
		return 0, err
	}
	return conn.LastInsertRowID(), nil
}

// SetGameUploaderActive sets whether the game's upload credential with the
// given ID may upload. It reports false if the game has no such credential.
func (s *Store) SetGameUploaderActive(ctx context.Context, gameID, uploaderID int64, active bool) (bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return false, err
	}
	defer s.pool.Put(conn)
	if err := sqlitex.Execute(conn, "UPDATE game_uploaders SET is_active = ? WHERE id = ? AND game_id = ?;", &sqlitex.ExecOptions{
		Args: []any{active, uploaderID, gameID},
	}); err != nil {
		return false, err
	}
	return conn.Changes() > 0, nil
}

// ResetGameUploader replaces the credential hash of the game's upload
// credential with the given ID, leaving its active flag alone.
// hashedCredentials computes the new hash from the account's current
// email, read in the same transaction. It returns that email, and reports
// false if the game has no such credential.
func (s *Store) ResetGameUploader(ctx context.Context, gameID, uploaderID int64, hashedCredentials func(email string) []byte) (email string, found bool, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return "", false, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	err = sqlitex.Execute(conn, `
SELECT a.email FROM game_uploaders gu JOIN accounts a ON a.id = gu.account_id
WHERE gu.id = ? AND gu.game_id = ?;`, &sqlitex.ExecOptions{
		Args: []any{uploaderID, gameID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			email, found = stmt.ColumnText(0), true
			return nil
		},
	})
	if err != nil || !found {
		return "", false, err
	}
	err = sqlitex.Execute(conn, "UPDATE game_uploaders SET hashed_credentials = ? WHERE id = ?;", &sqlitex.ExecOptions{
		Args: []any{hashedCredentials(email), uploaderID},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return "", false, ErrUploaderExists
	}
	return email, true, err
}
