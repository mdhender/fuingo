// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/mdhender/fuingo/internal/cerrs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

const (
	ErrAccountExists  = cerrs.Error("account already exists")
	ErrHasPassword    = cerrs.Error("the account already has a password")
	ErrCodeCase       = cerrs.Error("code must be lowercase")
	ErrEmailCase      = cerrs.Error("email must be lowercase")
	ErrGameExists     = cerrs.Error("game already exists in the system")
	ErrManagerExists  = cerrs.Error("account already manages the game")
	ErrPlayerExists   = cerrs.Error("account is already a player in the game")
	ErrSystemExists   = cerrs.Error("system already exists")
	ErrUnknownAccount = cerrs.Error("unknown account")
	ErrUnknownGame    = cerrs.Error("unknown game")
	ErrUnknownManager = cerrs.Error("account does not manage the game")
	ErrUnknownPlayer  = cerrs.Error("account is not a player in the game")
	ErrUnknownSystem  = cerrs.Error("unknown system")
)

// timeLayout stores UTC timestamps at a fixed width so they sort as text.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// GameRef names a game by the code of its system and its own code, which is
// unique only within that system. Both must already be lowercased.
type GameRef struct {
	System string
	Code   string
}

func (g GameRef) String() string {
	return g.System + "/" + g.Code
}

// NewAccount is an account to insert. Email must already be lowercased.
// HashedSecret is a bcrypt hash, or nil for an account that cannot sign in.
type NewAccount struct {
	Email        string
	HashedSecret []byte
}

// Account is an account. HashedSecret is nil if it cannot sign in.
type Account struct {
	ID           int64
	Email        string
	HashedSecret []byte
	Active       bool
}

// Member is an account's place in a game, as a player or a manager. Email
// must already be lowercased.
type Member struct {
	Game  GameRef
	Email string
}

// Rows are rows to insert together with CreateRows.
type Rows struct {
	Systems  []string
	Games    []GameRef
	Accounts []NewAccount
	Players  []Member
	Managers []Member
}

// CreateRows inserts systems, active games, accounts, active players, and
// active game managers in one transaction, in that order, so a player uses
// an account from rows.Accounts with the same email. It fails, inserting
// nothing, if any of them exists.
func (s *Store) CreateRows(ctx context.Context, rows Rows) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	for _, code := range rows.Systems {
		if _, err := createSystem(conn, code); err != nil {
			return err
		}
	}
	for _, g := range rows.Games {
		if _, err := createGame(conn, g); err != nil {
			return err
		}
	}
	for _, a := range rows.Accounts {
		if _, err := createAccount(conn, a); err != nil {
			return err
		}
	}
	for _, p := range rows.Players {
		if _, err := addPlayer(conn, p); err != nil {
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

// CreateSystem inserts a system and returns its ID. The code must be
// lowercase. It never modifies an existing row: a duplicate code fails with
// ErrSystemExists.
func (s *Store) CreateSystem(ctx context.Context, code string) (int64, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, err
	}
	defer s.pool.Put(conn)
	return createSystem(conn, code)
}

func createSystem(conn *sqlite.Conn, code string) (int64, error) {
	if code != strings.ToLower(code) {
		return 0, fmt.Errorf("%s: %w", code, ErrCodeCase)
	}
	err := sqlitex.Execute(conn, "INSERT INTO systems (code) VALUES (?);", &sqlitex.ExecOptions{
		Args: []any{code},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return 0, fmt.Errorf("%s: %w", code, ErrSystemExists)
	} else if err != nil {
		return 0, err
	}
	return conn.LastInsertRowID(), nil
}

// Systems returns the code of every system, in ID order.
func (s *Store) Systems(ctx context.Context) ([]string, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var codes []string
	err = sqlitex.Execute(conn, "SELECT code FROM systems ORDER BY id;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			codes = append(codes, stmt.ColumnText(0))
			return nil
		},
	})
	return codes, err
}

// CreateGame inserts an active game in an existing system and returns its
// ID. Both codes must be lowercase. It never modifies an existing row: a
// code already used in that system fails with ErrGameExists. Another system
// may use the same code.
func (s *Store) CreateGame(ctx context.Context, g GameRef) (int64, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, err
	}
	defer s.pool.Put(conn)
	return createGame(conn, g)
}

func createGame(conn *sqlite.Conn, g GameRef) (id int64, err error) {
	if g.Code != strings.ToLower(g.Code) {
		return 0, fmt.Errorf("%s: %w", g.Code, ErrCodeCase)
	}
	defer sqlitex.Save(conn)(&err)
	var systemID int64
	var found bool
	err = sqlitex.Execute(conn, "SELECT id FROM systems WHERE code = ?;", &sqlitex.ExecOptions{
		Args: []any{g.System},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			systemID, found = stmt.ColumnInt64(0), true
			return nil
		},
	})
	if err != nil {
		return 0, err
	} else if !found {
		return 0, fmt.Errorf("%s: %w", g.System, ErrUnknownSystem)
	}
	err = sqlitex.Execute(conn, "INSERT INTO games (system_id, code, is_active) VALUES (?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{systemID, g.Code},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return 0, fmt.Errorf("%s: %w", g, ErrGameExists)
	} else if err != nil {
		return 0, err
	}
	return conn.LastInsertRowID(), nil
}

// Games returns every game, in ID order.
func (s *Store) Games(ctx context.Context) ([]GameRef, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var games []GameRef
	err = sqlitex.Execute(conn, "SELECT s.code, g.code FROM games g JOIN systems s ON s.id = g.system_id ORDER BY g.id;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			games = append(games, GameRef{System: stmt.ColumnText(0), Code: stmt.ColumnText(1)})
			return nil
		},
	})
	return games, err
}

// GameID returns the ID of the game, active or not.
func (s *Store) GameID(ctx context.Context, g GameRef) (int64, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, false, err
	}
	defer s.pool.Put(conn)
	return gameID(conn, g)
}

func gameID(conn *sqlite.Conn, g GameRef) (int64, bool, error) {
	var id int64
	var found bool
	err := sqlitex.Execute(conn, `
SELECT g.id FROM games g JOIN systems s ON s.id = g.system_id
WHERE s.code = ? AND g.code = ?;`, &sqlitex.ExecOptions{
		Args: []any{g.System, g.Code},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			id, found = stmt.ColumnInt64(0), true
			return nil
		},
	})
	return id, found, err
}

// mustGameID is gameID, failing with ErrUnknownGame if there is none.
func mustGameID(conn *sqlite.Conn, g GameRef) (int64, error) {
	id, ok, err := gameID(conn, g)
	if err != nil {
		return 0, err
	} else if !ok {
		return 0, fmt.Errorf("%s: %w", g, ErrUnknownGame)
	}
	return id, nil
}

// SetGameActive sets whether a game is active.
func (s *Store) SetGameActive(ctx context.Context, g GameRef, active bool) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	id, err := mustGameID(conn, g)
	if err != nil {
		return err
	}
	return sqlitex.Execute(conn, "UPDATE games SET is_active = ? WHERE id = ?;", &sqlitex.ExecOptions{
		Args: []any{active, id},
	})
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

// SetAccountActive sets whether an account may sign in, act on the games it
// manages, and play in any game.
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

// SetAccountSecret replaces an account's hashed secret, a bcrypt hash, so it
// can sign in. The account may be one made for a player, without a secret,
// and may be active or not.
func (s *Store) SetAccountSecret(ctx context.Context, email string, hashedSecret []byte) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	if err := sqlitex.Execute(conn, "UPDATE accounts SET hashed_secret = ? WHERE email = ?;", &sqlitex.ExecOptions{
		Args: []any{string(hashedSecret), email},
	}); err != nil {
		return err
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("%s: %w", email, ErrUnknownAccount)
	}
	return nil
}

// AddedPlayer describes a player that was just added to a game. ID is the
// player's number. NewAccount reports that the email had no account and one
// was created for it, without a password: it needs an invitation.
type AddedPlayer struct {
	ID         int64
	AccountID  int64
	NewAccount bool
}

// AddPlayer makes the account with the email an active player in an existing
// game, creating the account, without a hashed secret, if the email has
// none. It fails with ErrPlayerExists if the account is already a player
// there, even an inactive one.
func (s *Store) AddPlayer(ctx context.Context, p Member) (AddedPlayer, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return AddedPlayer{}, err
	}
	defer s.pool.Put(conn)
	return addPlayer(conn, p)
}

func addPlayer(conn *sqlite.Conn, p Member) (added AddedPlayer, err error) {
	defer sqlitex.Save(conn)(&err)
	game, err := mustGameID(conn, p.Game)
	if err != nil {
		return AddedPlayer{}, err
	}
	return addGamePlayer(conn, game, p.Email)
}

// addGamePlayer adds the player inside the caller's transaction.
func addGamePlayer(conn *sqlite.Conn, gameID int64, email string) (AddedPlayer, error) {
	account, ok, err := accountID(conn, email)
	if err != nil {
		return AddedPlayer{}, err
	} else if !ok {
		if account, err = createAccount(conn, NewAccount{Email: email}); err != nil {
			return AddedPlayer{}, err
		}
	}
	err = sqlitex.Execute(conn, "INSERT INTO game_players (game_id, account_id, is_active) VALUES (?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{gameID, account},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return AddedPlayer{}, fmt.Errorf("%s: %w", email, ErrPlayerExists)
	} else if err != nil {
		return AddedPlayer{}, err
	}
	return AddedPlayer{ID: conn.LastInsertRowID(), AccountID: account, NewAccount: !ok}, nil
}

// SetPlayerActive sets whether an account's player in a game is active. It
// fails with ErrUnknownPlayer if the account was never a player there.
func (s *Store) SetPlayerActive(ctx context.Context, p Member, active bool) error {
	return s.setMemberActive(ctx, "game_players", ErrUnknownPlayer, p, active)
}

// AddManager makes an existing account an active manager of an existing
// game, active or not. It fails with ErrManagerExists if the account already
// manages the game, even as an inactive manager.
func (s *Store) AddManager(ctx context.Context, m Member) error {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return addManager(conn, m)
}

func addManager(conn *sqlite.Conn, m Member) (err error) {
	defer sqlitex.Save(conn)(&err)
	game, err := mustGameID(conn, m.Game)
	if err != nil {
		return err
	}
	account, ok, err := accountID(conn, m.Email)
	if err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s: %w", m.Email, ErrUnknownAccount)
	}
	err = sqlitex.Execute(conn, "INSERT INTO game_managers (game_id, account_id, is_active) VALUES (?, ?, 1);", &sqlitex.ExecOptions{
		Args: []any{game, account},
	})
	if sqlite.ErrCode(err) == sqlite.ResultConstraintUnique {
		return fmt.Errorf("%s %s: %w", m.Game, m.Email, ErrManagerExists)
	}
	return err
}

// SetManagerActive sets whether an account manages a game. It fails with
// ErrUnknownManager if the account was never made a manager of the game.
func (s *Store) SetManagerActive(ctx context.Context, m Member, active bool) error {
	return s.setMemberActive(ctx, "game_managers", ErrUnknownManager, m, active)
}

// setMemberActive sets is_active on the account's row for the game in
// table, which is game_players or game_managers.
func (s *Store) setMemberActive(ctx context.Context, table string, unknown error, m Member, active bool) (err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	game, err := mustGameID(conn, m.Game)
	if err != nil {
		return err
	}
	if err := sqlitex.Execute(conn, "UPDATE "+table+` SET is_active = ?
WHERE game_id = ? AND account_id = (SELECT id FROM accounts WHERE email = ?);`, &sqlitex.ExecOptions{
		Args: []any{active, game, m.Email},
	}); err != nil {
		return err
	}
	if conn.Changes() == 0 {
		return fmt.Errorf("%s %s: %w", m.Game, m.Email, unknown)
	}
	return nil
}

// Managers returns the emails of the game's active managers whose accounts
// are also active, in account ID order.
func (s *Store) Managers(ctx context.Context, g GameRef) ([]string, error) {
	return s.memberEmails(ctx, "active_managers", g)
}

// Players returns the emails of the game's active players whose accounts
// are also active, in account ID order. An inactive game has none.
func (s *Store) Players(ctx context.Context, g GameRef) ([]string, error) {
	return s.memberEmails(ctx, "active_players", g)
}

// memberEmails lists the emails in view, which is active_managers or
// active_players, for the game.
func (s *Store) memberEmails(ctx context.Context, view string, g GameRef) ([]string, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var emails []string
	err = sqlitex.Execute(conn, "SELECT m.email FROM "+view+` m
JOIN games g ON g.id = m.game_id JOIN systems s ON s.id = g.system_id
WHERE s.code = ? AND g.code = ?
ORDER BY m.account_id;`, &sqlitex.ExecOptions{
		Args: []any{g.System, g.Code},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			emails = append(emails, stmt.ColumnText(0))
			return nil
		},
	})
	return emails, err
}
