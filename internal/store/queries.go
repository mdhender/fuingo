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
	ErrAccountExists   = cerrs.Error("account already exists")
	ErrEmailCase       = cerrs.Error("email must be lowercase")
	ErrGameExists      = cerrs.Error("game already exists")
	ErrManagerExists   = cerrs.Error("account already manages the game")
	ErrUnknownAccount  = cerrs.Error("unknown account")
	ErrGameCodeCase    = cerrs.Error("game code must be lowercase")
	ErrUnknownGame     = cerrs.Error("unknown game")
	ErrUnknownManager  = cerrs.Error("account does not manage the game")
	ErrUnknownUploader = cerrs.Error("unknown uploader")
	ErrUploaderExists  = cerrs.Error("uploader already exists or credential is in use")
)

// timeLayout stores UTC timestamps at a fixed width so they sort as text.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// NewUploader is an upload credential to insert. GameCode and Email must
// already be lowercased. The account with the email is created, without a
// hashed secret, if it does not exist. HashedCredentials computes the
// credential hash (with package credential) from the game's ID, which is
// looked up in the same transaction as the insert.
type NewUploader struct {
	GameCode          string
	Email             string
	HashedCredentials func(gameID int64) []byte
}

// NewAccount is an account to insert. Email must already be lowercased.
// HashedSecret is a bcrypt hash, or nil for an account that cannot use the
// API.
type NewAccount struct {
	Email        string
	HashedSecret []byte
}

// Account is an account. HashedSecret is nil if it cannot use the API.
type Account struct {
	ID           int64
	Email        string
	HashedSecret []byte
	Active       bool
}

// NewManager makes an account a manager of a game. GameCode and Email
// must already be lowercased.
type NewManager struct {
	GameCode string
	Email    string
}

// Rows are rows to insert together with CreateRows.
type Rows struct {
	Games     []string
	Uploaders []NewUploader
	Accounts  []NewAccount
	Managers  []NewManager
}

// Uploader is an active upload credential of an active account in an
// active game.
type Uploader struct {
	ID                int64
	GameID            int64
	Email             string
	HashedCredentials []byte
}

// Upload is one row of the upload history.
type Upload struct {
	ID          int64
	UploaderID  int64
	GameID      int64
	GameCode    string
	Email       string
	TurnNo      string
	ContentHash string
	UploadedAt  time.Time
}

// CreateRows inserts active games, accounts, active upload credentials, and
// game managers in one transaction, in that order, so an uploader uses an
// account from rows.Accounts with the same email. It fails, inserting
// nothing, if any of them exists.
func (s *Store) CreateRows(ctx context.Context, rows Rows) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	for _, code := range rows.Games {
		if _, err := createGame(conn, code); err != nil {
			return err
		}
	}
	for _, a := range rows.Accounts {
		if _, err := createAccount(conn, a); err != nil {
			return err
		}
	}
	for _, u := range rows.Uploaders {
		if err := createUploader(conn, u); err != nil {
			return err
		}
	}
	for _, m := range rows.Managers {
		if err := addManager(conn, m); err != nil {
			return err
		}
	}
	return nil
}

// AddManager makes an existing account an active manager of an existing
// game, active or not. It fails with ErrManagerExists if the account already
// manages the game, even as an inactive manager.
func (s *Store) AddManager(ctx context.Context, m NewManager) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return addManager(conn, m)
}

func addManager(conn *sqlite.Conn, m NewManager) (err error) {
	defer sqlitex.Save(conn)(&err)
	gameID, ok, err := gameID(conn, m.GameCode)
	if err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s: %w", m.GameCode, ErrUnknownGame)
	}
	accountID, ok, err := accountID(conn, m.Email)
	if err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s: %w", m.Email, ErrUnknownAccount)
	}
	err = sqlitex.Execute(conn, "INSERT INTO game_managers (game_id, account_id, is_active) VALUES (?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{gameID, accountID},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return fmt.Errorf("%s %s: %w", m.GameCode, m.Email, ErrManagerExists)
	}
	return err
}

// Managers returns the emails of the game's active managers whose accounts
// are also active, in account ID order.
func (s *Store) Managers(ctx context.Context, gameCode string) ([]string, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var emails []string
	err = sqlitex.Execute(conn, `
SELECT m.email FROM active_managers m JOIN games g ON g.id = m.game_id
WHERE g.code = ?
ORDER BY m.account_id;`, &sqlitex.ExecOptions{
		Args: []any{gameCode},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			emails = append(emails, stmt.ColumnText(0))
			return nil
		},
	})
	return emails, err
}

// CreateAccount inserts an account and returns its ID. It never modifies
// an existing row: a duplicate email fails with ErrAccountExists.
func (s *Store) CreateAccount(ctx context.Context, a NewAccount) (int64, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, err
	}
	defer s.pool.Put(conn)
	return createAccount(conn, a)
}

func createAccount(conn *sqlite.Conn, a NewAccount) (int64, error) {
	if a.Email != strings.ToLower(a.Email) {
		return 0, fmt.Errorf("%s: %w", a.Email, ErrEmailCase)
	}
	var secret any // NULL without a hashed secret
	if a.HashedSecret != nil {
		secret = string(a.HashedSecret)
	}
	err := sqlitex.Execute(conn, "INSERT INTO accounts (email, hashed_secret, is_active) VALUES (?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{a.Email, secret},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return 0, fmt.Errorf("%s: %w", a.Email, ErrAccountExists)
	} else if err != nil {
		return 0, err
	}
	return conn.LastInsertRowID(), nil
}

// FindAccount returns the account with the given lowercased email,
// active or not.
func (s *Store) FindAccount(ctx context.Context, email string) (Account, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Account{}, false, err
	}
	defer s.pool.Put(conn)
	var a Account
	var found bool
	err = sqlitex.Execute(conn, "SELECT id, email, hashed_secret, is_active FROM accounts WHERE email = ?;", &sqlitex.ExecOptions{
		Args: []any{email},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			a = Account{ID: stmt.ColumnInt64(0), Email: stmt.ColumnText(1), Active: stmt.ColumnBool(3)}
			if stmt.ColumnType(2) != sqlite.TypeNull {
				a.HashedSecret = []byte(stmt.ColumnText(2))
			}
			found = true
			return nil
		},
	})
	return a, found, err
}

// CreateGame inserts an active game and returns its ID. The code must be
// lowercase. It never modifies an existing row: a duplicate code fails with
// ErrGameExists.
func (s *Store) CreateGame(ctx context.Context, code string) (int64, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, err
	}
	defer s.pool.Put(conn)
	return createGame(conn, code)
}

// CreateUploader inserts an active upload credential for an existing game,
// creating the account if it does not exist. It never modifies an existing
// row: a duplicate game and email, or a credential hash already in use,
// fails with ErrUploaderExists.
func (s *Store) CreateUploader(ctx context.Context, u NewUploader) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return createUploader(conn, u)
}

func createGame(conn *sqlite.Conn, code string) (int64, error) {
	if code != strings.ToLower(code) {
		return 0, fmt.Errorf("%s: %w", code, ErrGameCodeCase)
	}
	err := sqlitex.Execute(conn, "INSERT INTO games (code, is_active) VALUES (?, 1);", &sqlitex.ExecOptions{
		Args: []any{code},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return 0, fmt.Errorf("%s: %w", code, ErrGameExists)
	} else if err != nil {
		return 0, err
	}
	return conn.LastInsertRowID(), nil
}

func createUploader(conn *sqlite.Conn, u NewUploader) (err error) {
	defer sqlitex.Save(conn)(&err)
	gameID, ok, err := gameID(conn, u.GameCode)
	if err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s: %w", u.GameCode, ErrUnknownGame)
	}
	accountID, ok, err := accountID(conn, u.Email)
	if err != nil {
		return err
	} else if !ok {
		if accountID, err = createAccount(conn, NewAccount{Email: u.Email}); err != nil {
			return err
		}
	}
	err = sqlitex.Execute(conn, "INSERT INTO game_uploaders (game_id, account_id, hashed_credentials, is_active) VALUES (?, ?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{gameID, accountID, u.HashedCredentials(gameID)},
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

func accountID(conn *sqlite.Conn, email string) (int64, bool, error) {
	var id int64
	var found bool
	err := sqlitex.Execute(conn, "SELECT id FROM accounts WHERE email = ?;", &sqlitex.ExecOptions{
		Args: []any{email},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			id, found = stmt.ColumnInt64(0), true
			return nil
		},
	})
	return id, found, err
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

// FindUploader returns the upload credential with the given credential
// hash. It reports false if there is none, or if it, its account, or its
// game is inactive.
func (s *Store) FindUploader(ctx context.Context, credentialHash []byte) (Uploader, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Uploader{}, false, err
	}
	defer s.pool.Put(conn)
	var u Uploader
	var found bool
	err = sqlitex.Execute(conn, `
SELECT id, game_id, email, hashed_credentials FROM active_uploaders
WHERE hashed_credentials = ?;`, &sqlitex.ExecOptions{
		Args: []any{credentialHash},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			found = true
			u.ID = stmt.ColumnInt64(0)
			u.GameID = stmt.ColumnInt64(1)
			u.Email = stmt.ColumnText(2)
			u.HashedCredentials = make([]byte, stmt.ColumnLen(3))
			stmt.ColumnBytes(3, u.HashedCredentials)
			return nil
		},
	})
	if err != nil {
		return Uploader{}, false, err
	}
	return u, found, nil
}

// RecordUpload stores contents under contentHash and inserts a row in the
// upload history for the uploader's turn, recording the email the upload
// arrived with, in one transaction. Contents already stored under that hash
// are kept as they are, so every upload of identical bytes shares one blob.
func (s *Store) RecordUpload(ctx context.Context, uploaderID int64, email, turnNo, contentHash string, contents []byte, at time.Time) (err error) {
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
	return sqlitex.Execute(conn, "INSERT INTO uploads (uploader_id, email, turn_no, content_hash, uploaded_at) VALUES (?, ?, ?, ?, ?);", &sqlitex.ExecOptions{
		Args: []any{uploaderID, email, turnNo, contentHash, uploadedAt},
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
SELECT u.id, u.uploader_id, gu.game_id, g.code, u.email, u.turn_no, u.content_hash, u.uploaded_at
FROM uploads u
JOIN game_uploaders gu ON gu.id = u.uploader_id
JOIN games g ON g.id = gu.game_id
ORDER BY u.id;`, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			at, err := time.Parse(timeLayout, stmt.ColumnText(7))
			if err != nil {
				return err
			}
			uploads = append(uploads, Upload{
				ID:          stmt.ColumnInt64(0),
				UploaderID:  stmt.ColumnInt64(1),
				GameID:      stmt.ColumnInt64(2),
				GameCode:    stmt.ColumnText(3),
				Email:       stmt.ColumnText(4),
				TurnNo:      stmt.ColumnText(5),
				ContentHash: stmt.ColumnText(6),
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

// SetUploaderActive sets whether an upload credential may upload.
func (s *Store) SetUploaderActive(ctx context.Context, gameCode, email string, active bool) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	if err := sqlitex.Execute(conn, `
UPDATE game_uploaders SET is_active = ?
WHERE account_id = (SELECT id FROM accounts WHERE email = ?)
  AND game_id = (SELECT id FROM games WHERE code = ?);`, &sqlitex.ExecOptions{
		Args: []any{active, email, gameCode},
	}); err != nil {
		return err
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("%s %s: %w", gameCode, email, ErrUnknownUploader)
	}
	return nil
}

// SetManagerActive sets whether an account manages a game. It fails with
// ErrUnknownManager if the account was never made a manager of the game.
func (s *Store) SetManagerActive(ctx context.Context, gameCode, email string, active bool) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	if err := sqlitex.Execute(conn, `
UPDATE game_managers SET is_active = ?
WHERE game_id = (SELECT id FROM games WHERE code = ?)
  AND account_id = (SELECT id FROM accounts WHERE email = ?);`, &sqlitex.ExecOptions{
		Args: []any{active, gameCode, email},
	}); err != nil {
		return err
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("%s %s: %w", gameCode, email, ErrUnknownManager)
	}
	return nil
}

// SetAccountActive sets whether an account may authenticate, act on the
// games it manages, and upload.
func (s *Store) SetAccountActive(ctx context.Context, email string, active bool) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	if err := sqlitex.Execute(conn, "UPDATE accounts SET is_active = ? WHERE email = ?;", &sqlitex.ExecOptions{
		Args: []any{active, email},
	}); err != nil {
		return err
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("%s: %w", email, ErrUnknownAccount)
	}
	return nil
}
