// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// ErrTurnClosed is returned when a player uploads to, or withdraws from, a
// turn that is closed.
const ErrTurnClosed = cerrs.Error("the turn is closed")

// Access is what an account may reach in one game: as an active manager,
// as an active player, or both. Player is the player's number, or 0 if the
// account is not an active player of the game.
type Access struct {
	Game    GameRef
	GameID  int64
	Active  bool // the game's own flag
	Manager bool
	Player  int64
}

const selectAccess = `
SELECT s.code, g.code, g.id, g.is_active,
       EXISTS (SELECT 1 FROM active_managers m WHERE m.game_id = g.id AND m.account_id = ?1),
       coalesce((SELECT p.id FROM active_players p WHERE p.game_id = g.id AND p.account_id = ?1), 0)
FROM games g JOIN systems s ON s.id = g.system_id
WHERE g.id IN (SELECT game_id FROM active_managers WHERE account_id = ?1
               UNION SELECT game_id FROM active_players WHERE account_id = ?1)
`

func scanAccess(stmt *sqlite.Stmt) Access {
	return Access{
		Game:    GameRef{System: stmt.ColumnText(0), Code: stmt.ColumnText(1)},
		GameID:  stmt.ColumnInt64(2),
		Active:  stmt.ColumnBool(3),
		Manager: stmt.ColumnBool(4),
		Player:  stmt.ColumnInt64(5),
	}
}

// AccountGames returns every game the account may reach, as an active
// manager or an active player, in order of system code and then game code.
// A player's game must be active; a manager's need not be.
func (s *Store) AccountGames(ctx context.Context, accountID int64) ([]Access, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var games []Access
	err = sqlitex.Execute(conn, selectAccess+"ORDER BY s.code, g.code;", &sqlitex.ExecOptions{
		Args: []any{accountID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			games = append(games, scanAccess(stmt))
			return nil
		},
	})
	return games, err
}

// GameAccess returns what the account may reach in the game. It reports
// false if the account is neither an active manager nor an active player of
// it, which includes a game that does not exist.
func (s *Store) GameAccess(ctx context.Context, accountID int64, g GameRef) (Access, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Access{}, false, err
	}
	defer s.pool.Put(conn)
	var a Access
	var found bool
	err = sqlitex.Execute(conn, selectAccess+"AND s.code = ?2 AND g.code = ?3;", &sqlitex.ExecOptions{
		Args: []any{accountID, g.System, g.Code},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			a, found = scanAccess(stmt), true
			return nil
		},
	})
	return a, found, err
}

// FindTurn returns the game's turn with the given number. It reports false
// if the game has none.
func (s *Store) FindTurn(ctx context.Context, gameID, number int64) (Turn, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Turn{}, false, err
	}
	defer s.pool.Put(conn)
	var t Turn
	var found bool
	err = sqlitex.Execute(conn, "SELECT turn_no, created_at, closed_at FROM turns WHERE game_id = ? AND turn_no = ?;", &sqlitex.ExecOptions{
		Args: []any{gameID, number},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			t, found = scanTurn(stmt), true
			return nil
		},
	})
	return t, found, err
}

// TurnFolderCounts returns, for each player with files in their folder in
// the game's turn, how many of them are not withdrawn, keyed by the
// player's number.
func (s *Store) TurnFolderCounts(ctx context.Context, gameID, turn int64) (map[int64]int, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	counts := map[int64]int{}
	err = sqlitex.Execute(conn, `
SELECT u.player_id, count(*) FROM uploads u JOIN turns t ON t.id = u.turn_id
WHERE u.game_id = ? AND t.turn_no = ? AND u.player_id IS NOT NULL AND u.withdrawn_at IS NULL
GROUP BY u.player_id;`, &sqlitex.ExecOptions{
		Args: []any{gameID, turn},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			counts[stmt.ColumnInt64(0)] = stmt.ColumnInt(1)
			return nil
		},
	})
	return counts, err
}

// WithdrawPlayerUpload withdraws a file from the player's own folder, as
// that player, and returns its record. The file must be in the game, in
// that player's folder, and not already withdrawn, or it fails with
// ErrUnknownUpload: a player is not told of any other file. A file the
// account did not upload fails with ErrNotUploader, and one in a closed
// turn with ErrTurnClosed. The checks and the change are one transaction.
// Nothing is deleted.
func (s *Store) WithdrawPlayerUpload(ctx context.Context, gameID, id, accountID, playerID int64, now time.Time) (u Upload, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer s.pool.Put(conn)
	end, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return Upload{}, err
	}
	defer end(&err)
	var found, mine, closed bool
	err = sqlitex.Execute(conn, `
SELECT u.uploader_account_id = ?1, t.closed_at IS NOT NULL
FROM uploads u JOIN turns t ON t.id = u.turn_id
WHERE u.id = ?2 AND u.game_id = ?3 AND u.player_id = ?4 AND u.withdrawn_at IS NULL;`, &sqlitex.ExecOptions{
		Args: []any{accountID, id, gameID, playerID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			found, mine, closed = true, stmt.ColumnBool(0), stmt.ColumnBool(1)
			return nil
		},
	})
	switch {
	case err != nil:
		return Upload{}, err
	case !found:
		return Upload{}, ErrUnknownUpload
	case !mine:
		return Upload{}, ErrNotUploader
	case closed:
		return Upload{}, ErrTurnClosed
	}
	err = sqlitex.Execute(conn, "UPDATE uploads SET withdrawn_at = ? WHERE id = ?;", &sqlitex.ExecOptions{
		Args: []any{now.UTC().Format(timeLayout), id},
	})
	if err != nil {
		return Upload{}, err
	}
	u, _, err = gameUpload(conn, gameID, id)
	return u, err
}
