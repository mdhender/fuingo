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

// ManagedGameID returns the ID of the game if the account actively manages
// it. The game may be active or not.
func (s *Store) ManagedGameID(ctx context.Context, accountID int64, g GameRef) (int64, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, false, err
	}
	defer s.pool.Put(conn)
	var id int64
	var found bool
	err = sqlitex.Execute(conn, `
SELECT g.id FROM games g
JOIN systems s ON s.id = g.system_id
JOIN active_managers m ON m.game_id = g.id
WHERE s.code = ? AND g.code = ? AND m.account_id = ?;`, &sqlitex.ExecOptions{
		Args: []any{g.System, g.Code, accountID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			id, found = stmt.ColumnInt64(0), true
			return nil
		},
	})
	return id, found, err
}

// GameInfo describes a game.
type GameInfo struct {
	Game   GameRef
	Active bool
}

// ManagedGames returns the games the account actively manages, active or
// not, in order of system code and then game code.
func (s *Store) ManagedGames(ctx context.Context, accountID int64) ([]GameInfo, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var games []GameInfo
	err = sqlitex.Execute(conn, `
SELECT s.code, g.code, g.is_active FROM games g
JOIN systems s ON s.id = g.system_id
JOIN active_managers m ON m.game_id = g.id
WHERE m.account_id = ?
ORDER BY s.code, g.code;`, &sqlitex.ExecOptions{
		Args: []any{accountID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			games = append(games, GameInfo{
				Game:   GameRef{System: stmt.ColumnText(0), Code: stmt.ColumnText(1)},
				Active: stmt.ColumnBool(2),
			})
			return nil
		},
	})
	return games, err
}

// PlayerInfo describes one player of a game. Active is the player's own
// flag; AccountActive is its account's. Both, and the game, must be active
// for the player to use the game.
type PlayerInfo struct {
	ID            int64
	Email         string
	Active        bool
	AccountActive bool
}

// GamePlayers returns the game's players, active or not, in order of ID.
func (s *Store) GamePlayers(ctx context.Context, gameID int64) ([]PlayerInfo, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var players []PlayerInfo
	err = sqlitex.Execute(conn, `
SELECT gp.id, a.email, gp.is_active, a.is_active
FROM game_players gp JOIN accounts a ON a.id = gp.account_id
WHERE gp.game_id = ?
ORDER BY gp.id;`, &sqlitex.ExecOptions{
		Args: []any{gameID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			players = append(players, PlayerInfo{
				ID:            stmt.ColumnInt64(0),
				Email:         stmt.ColumnText(1),
				Active:        stmt.ColumnBool(2),
				AccountActive: stmt.ColumnBool(3),
			})
			return nil
		},
	})
	return players, err
}

// AddGamePlayer makes the account with the lowercased email an active
// player in the game and returns the player's ID, creating the account,
// without a hashed secret, if the email has none. An account that is
// already a player there fails with ErrPlayerExists.
func (s *Store) AddGamePlayer(ctx context.Context, gameID int64, email string) (id int64, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return 0, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	return addGamePlayer(conn, gameID, email)
}

// SetGamePlayerActive sets whether the game's player with the given ID is
// active. It reports false if the game has no such player.
func (s *Store) SetGamePlayerActive(ctx context.Context, gameID, playerID int64, active bool) (bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return false, err
	}
	defer s.pool.Put(conn)
	if err := sqlitex.Execute(conn, "UPDATE game_players SET is_active = ? WHERE id = ? AND game_id = ?;", &sqlitex.ExecOptions{
		Args: []any{active, playerID, gameID},
	}); err != nil {
		return false, err
	}
	return conn.Changes() > 0, nil
}
