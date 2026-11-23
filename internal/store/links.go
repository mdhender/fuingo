// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// The kinds of link mailed to an account.
const (
	LinkInvitation = "invitation" // for an account that has never had a password
	LinkReset      = "reset"
)

// CreateAccountLink records a link for the account, keyed by the SHA-256 of
// its token, and cancels the account's earlier links that have not been
// used, so only the newest link works.
func (s *Store) CreateAccountLink(ctx context.Context, accountID int64, kind string, hashedToken []byte, now, expiresAt time.Time) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	at := now.UTC().Format(timeLayout)
	err = sqlitex.Execute(conn, "UPDATE account_links SET canceled_at = ? WHERE account_id = ? AND used_at IS NULL AND canceled_at IS NULL;", &sqlitex.ExecOptions{
		Args: []any{at, accountID},
	})
	if err != nil {
		return err
	}
	return sqlitex.Execute(conn, "INSERT INTO account_links (account_id, kind, hashed_token, created_at, expires_at) VALUES (?, ?, ?, ?, ?);", &sqlitex.ExecOptions{
		Args: []any{accountID, kind, hashedToken, at, expiresAt.UTC().Format(timeLayout)},
	})
}

// liveLink finds the link with the token hash if it can still set a
// password: not used, not canceled, not expired by now, and its account
// active.
const liveLink = `
SELECT l.id, a.id, a.email, l.kind
FROM account_links l JOIN accounts a ON a.id = l.account_id
WHERE l.hashed_token = ?1 AND l.used_at IS NULL AND l.canceled_at IS NULL
  AND l.expires_at > ?2 AND a.is_active = 1;`

// LinkAccount describes the account a link belongs to.
type LinkAccount struct {
	AccountID int64
	Email     string
	Kind      string
}

// AccountLink returns the account of the link with the given token hash. It
// reports false unless the link can still set a password: one that does not
// exist, was used, was canceled by a newer link, has expired by now, or
// belongs to an inactive account is not found.
func (s *Store) AccountLink(ctx context.Context, hashedToken []byte, now time.Time) (LinkAccount, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return LinkAccount{}, false, err
	}
	defer s.pool.Put(conn)
	_, la, found, err := findLiveLink(conn, hashedToken, now)
	return la, found, err
}

func findLiveLink(conn *sqlite.Conn, hashedToken []byte, now time.Time) (linkID int64, la LinkAccount, found bool, err error) {
	err = sqlitex.Execute(conn, liveLink, &sqlitex.ExecOptions{
		Args: []any{hashedToken, now.UTC().Format(timeLayout)},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			linkID = stmt.ColumnInt64(0)
			la = LinkAccount{AccountID: stmt.ColumnInt64(1), Email: stmt.ColumnText(2), Kind: stmt.ColumnText(3)}
			found = true
			return nil
		},
	})
	return linkID, la, found, err
}

// UseAccountLink sets the password hash of the account that the link with
// the given token hash belongs to, marks the link used, and ends every API
// and web session of the account, all in one transaction. It reports false, and
// changes nothing, unless the link can still set a password, as in
// AccountLink. A link works once.
func (s *Store) UseAccountLink(ctx context.Context, hashedToken, hashedSecret []byte, now time.Time) (la LinkAccount, used bool, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return LinkAccount{}, false, err
	}
	defer s.pool.Put(conn)
	// The write lock is taken first, so two requests with one link cannot
	// both find it unused.
	end, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return LinkAccount{}, false, err
	}
	defer end(&err)
	linkID, la, found, err := findLiveLink(conn, hashedToken, now)
	if err != nil || !found {
		return LinkAccount{}, false, err
	}
	at := now.UTC().Format(timeLayout)
	for _, q := range []struct {
		query string
		args  []any
	}{
		{"UPDATE account_links SET used_at = ? WHERE id = ?;", []any{at, linkID}},
		{"UPDATE accounts SET hashed_secret = ? WHERE id = ?;", []any{string(hashedSecret), la.AccountID}},
		{"DELETE FROM api_sessions WHERE account_id = ?;", []any{la.AccountID}},
		{"DELETE FROM web_sessions WHERE account_id = ?;", []any{la.AccountID}},
	} {
		if err := sqlitex.Execute(conn, q.query, &sqlitex.ExecOptions{Args: q.args}); err != nil {
			return LinkAccount{}, false, err
		}
	}
	return la, true, nil
}

// CreateTestAccount gives the account with the given lowercased email the
// password hash, creating the account, active, if the email has none. It
// reports whether it created the account. An account that already has a
// password is refused with ErrHasPassword and nothing changes: this never
// replaces a password. An existing account keeps its is_active, and its
// unused links are canceled, so an invitation still in the mail cannot
// replace the password. All in one transaction.
func (s *Store) CreateTestAccount(ctx context.Context, email string, hashedSecret []byte, now time.Time) (id int64, created bool, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, false, err
	}
	defer s.pool.Put(conn)
	end, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return 0, false, err
	}
	defer end(&err)
	id, found, err := accountID(conn, email)
	if err != nil {
		return 0, false, err
	}
	if !found {
		id, err = createAccount(conn, NewAccount{Email: email, HashedSecret: hashedSecret})
		return id, err == nil, err
	}
	err = sqlitex.Execute(conn, "UPDATE accounts SET hashed_secret = ? WHERE id = ? AND hashed_secret IS NULL;", &sqlitex.ExecOptions{
		Args: []any{string(hashedSecret), id},
	})
	if err != nil {
		return 0, false, err
	}
	if conn.Changes() == 0 {
		return 0, false, fmt.Errorf("%s: %w", email, ErrHasPassword)
	}
	err = sqlitex.Execute(conn, "UPDATE account_links SET canceled_at = ? WHERE account_id = ? AND used_at IS NULL AND canceled_at IS NULL;", &sqlitex.ExecOptions{
		Args: []any{now.UTC().Format(timeLayout), id},
	})
	return id, false, err
}
