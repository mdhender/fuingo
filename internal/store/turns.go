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

// ErrNotNextTurn is returned by CreateTurn for a number that is not the
// game's next turn.
const ErrNotNextTurn = cerrs.Error("not the game's next turn")

// Turn is a turn of a game. ClosedAt is the zero time while it is open.
type Turn struct {
	Number    int64
	CreatedAt time.Time
	ClosedAt  time.Time
}

// Closed reports whether the turn is closed.
func (t Turn) Closed() bool {
	return !t.ClosedAt.IsZero()
}

// CreateTurn creates the game's turn with the given number, open, and
// returns it. The number must be the game's next: 0 for its first turn,
// then one more than its last. Any other number, including one that exists,
// fails with ErrNotNextTurn, and the returned Turn holds only the number
// that would have been accepted.
func (s *Store) CreateTurn(ctx context.Context, gameID, number int64, now time.Time) (Turn, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Turn{}, err
	}
	defer s.pool.Put(conn)
	created := now.UTC().Format(timeLayout)
	// One statement checks and inserts, so concurrent requests cannot both
	// create the turn or leave a gap.
	err = sqlitex.Execute(conn, `
INSERT INTO turns (game_id, turn_no, created_at)
SELECT ?1, ?2, ?3
WHERE ?2 = (SELECT coalesce(max(turn_no) + 1, 0) FROM turns WHERE game_id = ?1);`, &sqlitex.ExecOptions{
		Args: []any{gameID, number, created},
	})
	if err == nil && conn.Changes() > 0 {
		return Turn{Number: number, CreatedAt: parseTime(created)}, nil
	} else if err != nil && sqlite.ErrCode(err) != sqlite.ResultConstraintUnique {
		return Turn{}, err
	}
	var next int64
	err = sqlitex.Execute(conn, "SELECT coalesce(max(turn_no) + 1, 0) FROM turns WHERE game_id = ?;", &sqlitex.ExecOptions{
		Args: []any{gameID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			next = stmt.ColumnInt64(0)
			return nil
		},
	})
	if err != nil {
		return Turn{}, err
	}
	return Turn{Number: next}, ErrNotNextTurn
}

// Turns returns the game's turns, in order of number.
func (s *Store) Turns(ctx context.Context, gameID int64) ([]Turn, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var turns []Turn
	err = sqlitex.Execute(conn, "SELECT turn_no, created_at, closed_at FROM turns WHERE game_id = ? ORDER BY turn_no;", &sqlitex.ExecOptions{
		Args: []any{gameID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			turns = append(turns, scanTurn(stmt))
			return nil
		},
	})
	return turns, err
}

// SetTurnClosed closes or reopens the game's turn with the given number and
// returns it. Closing a closed turn keeps the time it was first closed;
// reopening an open turn changes nothing. It reports false if the game has
// no such turn.
func (s *Store) SetTurnClosed(ctx context.Context, gameID, number int64, closed bool, now time.Time) (turn Turn, found bool, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Turn{}, false, err
	}
	defer s.pool.Put(conn)
	var closedAt any // NULL reopens the turn
	if closed {
		closedAt = now.UTC().Format(timeLayout)
	}
	err = sqlitex.Execute(conn, `
UPDATE turns SET closed_at = CASE WHEN ?1 IS NULL THEN NULL ELSE coalesce(closed_at, ?1) END
WHERE game_id = ?2 AND turn_no = ?3
RETURNING turn_no, created_at, closed_at;`, &sqlitex.ExecOptions{
		Args: []any{closedAt, gameID, number},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			turn, found = scanTurn(stmt), true
			return nil
		},
	})
	return turn, found, err
}

// scanTurn reads a row of turn_no, created_at, closed_at.
func scanTurn(stmt *sqlite.Stmt) Turn {
	t := Turn{Number: stmt.ColumnInt64(0), CreatedAt: parseTime(stmt.ColumnText(1))}
	if stmt.ColumnType(2) != sqlite.TypeNull {
		t.ClosedAt = parseTime(stmt.ColumnText(2))
	}
	return t
}

// parseTime reads a timestamp this package stored. One it did not write
// reads as the zero time.
func parseTime(s string) time.Time {
	t, _ := time.Parse(timeLayout, s)
	return t
}
