// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// CreateWebSession stores a web session for the account, keyed by the
// SHA-256 of the token in its cookie, and deletes every web session that has
// expired by now.
func (s *Store) CreateWebSession(ctx context.Context, accountID int64, hashedToken []byte, now, expiresAt time.Time) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	at := now.UTC().Format(timeLayout)
	if err := sqlitex.Execute(conn, "DELETE FROM web_sessions WHERE expires_at <= ?;", &sqlitex.ExecOptions{Args: []any{at}}); err != nil {
		return err
	}
	return sqlitex.Execute(conn, "INSERT INTO web_sessions (account_id, hashed_token, created_at, last_used_at, expires_at) VALUES (?, ?, ?, ?, ?);", &sqlitex.ExecOptions{
		Args: []any{accountID, hashedToken, at, at, expiresAt.UTC().Format(timeLayout)},
	})
}

// UseWebSession returns the account of the web session with the given token
// hash and renews the session, so that it expires at expiresAt. It reports
// false, and renews nothing, if there is no such session, if it has expired
// by now, or if its account is inactive or has no hashed secret. An
// inactive account's session is kept, and works again if the account is
// enabled before it expires.
func (s *Store) UseWebSession(ctx context.Context, hashedToken []byte, now, expiresAt time.Time) (a Account, found bool, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Account{}, false, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	at := now.UTC().Format(timeLayout)
	var sessionID int64
	err = sqlitex.Execute(conn, `
SELECT s.id, a.id, a.email, a.hashed_secret
FROM web_sessions s JOIN accounts a ON a.id = s.account_id
WHERE s.hashed_token = ? AND s.expires_at > ?
  AND a.is_active = 1 AND a.hashed_secret IS NOT NULL;`, &sqlitex.ExecOptions{
		Args: []any{hashedToken, at},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			sessionID = stmt.ColumnInt64(0)
			a = Account{ID: stmt.ColumnInt64(1), Email: stmt.ColumnText(2), HashedSecret: []byte(stmt.ColumnText(3)), Active: true}
			found = true
			return nil
		},
	})
	if err != nil || !found {
		return Account{}, false, err
	}
	err = sqlitex.Execute(conn, "UPDATE web_sessions SET last_used_at = ?, expires_at = ? WHERE id = ?;", &sqlitex.ExecOptions{
		Args: []any{at, expiresAt.UTC().Format(timeLayout), sessionID},
	})
	if err != nil {
		return Account{}, false, err
	}
	return a, true, nil
}

// EndWebSession deletes the web session with the given token hash, if there
// is one.
func (s *Store) EndWebSession(ctx context.Context, hashedToken []byte) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return sqlitex.Execute(conn, "DELETE FROM web_sessions WHERE hashed_token = ?;", &sqlitex.ExecOptions{Args: []any{hashedToken}})
}
