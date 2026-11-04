// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gmstore is olympia's own database: the games a game master runs
// and their players, each an Olympia G3 faction played by a fuingo
// uploader. It follows the file contract in SQLITE3.md through package
// sqlitedb, with its own application ID, so it can never be mistaken for a
// fuingo database.
//
// Rows are identified by integer IDs, never by game codes or factions,
// which can change. Rows that others refer to cannot be deleted; disable
// them instead.
package gmstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/sqlitedb"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// applicationID is the ASCII bytes "olym" as a big-endian 32-bit integer.
const applicationID int32 = 0x6F6C796D

const (
	ErrGameExists    = cerrs.Error("game already exists")
	ErrGameInUse     = cerrs.Error("game has players; disable it instead, or delete its players first")
	ErrPlayerExists  = cerrs.Error("the game already has a player with that faction")
	ErrUnknownGame   = cerrs.Error("unknown game")
	ErrUnknownPlayer = cerrs.Error("unknown player")
	ErrPlayerInUse   = cerrs.Error("player is in use; disable it instead")
)

var migrations = []string{
	// 1: games and their players.
	`
CREATE TABLE games (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	code      TEXT    NOT NULL UNIQUE CHECK (code = lower(code) AND length(code) > 0),
	is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1))
);

-- faction is the Olympia G3 player entity code, as on a BEGIN line. email
-- is the fuingo uploader who plays it; one email may play several factions.
CREATE TABLE players (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id   INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	faction   TEXT    NOT NULL CHECK (faction = lower(faction) AND length(faction) > 0),
	email     TEXT    NOT NULL CHECK (email = lower(email) AND length(email) > 0),
	name      TEXT    NOT NULL DEFAULT '',
	is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	UNIQUE (game_id, faction)
);

CREATE INDEX players_by_email ON players (game_id, email);
`,
}

var schema = sqlitedb.Schema{
	AppID:       applicationID,
	Owner:       "olympia",
	MigrateHint: "run olympia store migrate",
	Migrations:  migrations,
}

// Store is a handle to an olympia database. It is safe for concurrent use.
type Store struct {
	db   *sqlitedb.DB
	pool *sqlitex.Pool
}

func wrap(db *sqlitedb.DB, err error) (*Store, error) {
	if err != nil {
		return nil, err
	}
	return &Store{db: db, pool: db.Pool}, nil
}

// Create makes a new database at path; the file must not exist.
func Create(ctx context.Context, path string) (*Store, error) {
	return wrap(schema.Create(ctx, path))
}

// Open opens an existing database at the current schema version.
func Open(ctx context.Context, path string) (*Store, error) {
	return wrap(schema.Open(ctx, path))
}

// Migrate brings an existing database up to the current schema.
func Migrate(ctx context.Context, path string) error {
	return schema.Migrate(ctx, path)
}

// OpenPrivateMemory creates a fresh, private in-memory database for tests.
func OpenPrivateMemory(ctx context.Context, name string) (*Store, error) {
	return wrap(schema.OpenPrivateMemory(ctx, name))
}

// CurrentSchemaVersion is the schema version this program expects.
func CurrentSchemaVersion() int {
	return schema.CurrentVersion()
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Game is a game. Code matches the game's code in fuingo.
type Game struct {
	ID      int64
	Code    string
	Active  bool
	Players int
}

// Player is a faction in a game.
type Player struct {
	ID       int64
	GameCode string
	Faction  string
	Email    string
	Name     string
	Active   bool
}

// PlayerChange holds the fields to change; nil fields are kept.
type PlayerChange struct {
	Faction *string
	Email   *string
	Name    *string
}

func (s *Store) with(ctx context.Context, fn func(*sqlite.Conn) error) error {
	conn, err := s.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer s.pool.Put(conn)
	return fn(conn)
}

func (s *Store) tx(ctx context.Context, fn func(*sqlite.Conn) error) error {
	return s.with(ctx, func(conn *sqlite.Conn) (err error) {
		defer sqlitex.Save(conn)(&err)
		return fn(conn)
	})
}

// isForeignKey reports whether err is a foreign-key violation. Deleting a
// referenced row reports plain SQLITE_CONSTRAINT rather than the extended
// code, so the message is checked too.
func isForeignKey(err error) bool {
	code := sqlite.ErrCode(err)
	return code == sqlite.ResultConstraintForeignKey ||
		(code.ToPrimary() == sqlite.ResultConstraint && strings.Contains(err.Error(), "FOREIGN KEY"))
}

func isUnique(err error) bool {
	code := sqlite.ErrCode(err)
	return code == sqlite.ResultConstraintUnique || code == sqlite.ResultConstraintPrimaryKey
}

// AddGame inserts an active game. The code must be lowercase.
func (s *Store) AddGame(ctx context.Context, code string) error {
	return s.with(ctx, func(conn *sqlite.Conn) error {
		err := sqlitex.Execute(conn, "INSERT INTO games (code) VALUES (?);", &sqlitex.ExecOptions{Args: []any{code}})
		if isUnique(err) {
			return fmt.Errorf("%s: %w", code, ErrGameExists)
		}
		return err
	})
}

const gameColumns = `g.id, g.code, g.is_active, (SELECT count(*) FROM players p WHERE p.game_id = g.id)`

func scanGame(stmt *sqlite.Stmt) Game {
	return Game{ID: stmt.ColumnInt64(0), Code: stmt.ColumnText(1), Active: stmt.ColumnBool(2), Players: stmt.ColumnInt(3)}
}

// Games returns every game, in code order.
func (s *Store) Games(ctx context.Context) ([]Game, error) {
	var games []Game
	err := s.with(ctx, func(conn *sqlite.Conn) error {
		return sqlitex.Execute(conn, "SELECT "+gameColumns+" FROM games g ORDER BY g.code;", &sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				games = append(games, scanGame(stmt))
				return nil
			},
		})
	})
	return games, err
}

// Game returns the game with the code.
func (s *Store) Game(ctx context.Context, code string) (Game, error) {
	var g Game
	err := s.with(ctx, func(conn *sqlite.Conn) error {
		var err error
		g, err = findGame(conn, code)
		return err
	})
	return g, err
}

func findGame(conn *sqlite.Conn, code string) (Game, error) {
	var g Game
	found := false
	err := sqlitex.Execute(conn, "SELECT "+gameColumns+" FROM games g WHERE g.code = ?;", &sqlitex.ExecOptions{
		Args: []any{code},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			g, found = scanGame(stmt), true
			return nil
		},
	})
	if err == nil && !found {
		err = fmt.Errorf("%s: %w", code, ErrUnknownGame)
	}
	return g, err
}

// RenameGame changes a game's code. Its players keep belonging to it.
func (s *Store) RenameGame(ctx context.Context, code, newCode string) error {
	return s.with(ctx, func(conn *sqlite.Conn) error {
		err := sqlitex.Execute(conn, "UPDATE games SET code = ? WHERE code = ?;", &sqlitex.ExecOptions{Args: []any{newCode, code}})
		if isUnique(err) {
			return fmt.Errorf("%s: %w", newCode, ErrGameExists)
		} else if err != nil {
			return err
		}
		if conn.Changes() == 0 {
			return fmt.Errorf("%s: %w", code, ErrUnknownGame)
		}
		return nil
	})
}

// SetGameActive sets whether a game is active. Repeating it succeeds.
func (s *Store) SetGameActive(ctx context.Context, code string, active bool) error {
	return s.with(ctx, func(conn *sqlite.Conn) error {
		if err := sqlitex.Execute(conn, "UPDATE games SET is_active = ? WHERE code = ?;", &sqlitex.ExecOptions{Args: []any{active, code}}); err != nil {
			return err
		}
		if conn.Changes() == 0 {
			return fmt.Errorf("%s: %w", code, ErrUnknownGame)
		}
		return nil
	})
}

// DeleteGame deletes a game that has no players.
func (s *Store) DeleteGame(ctx context.Context, code string) error {
	return s.with(ctx, func(conn *sqlite.Conn) error {
		err := sqlitex.Execute(conn, "DELETE FROM games WHERE code = ?;", &sqlitex.ExecOptions{Args: []any{code}})
		if isForeignKey(err) {
			return fmt.Errorf("%s: %w", code, ErrGameInUse)
		} else if err != nil {
			return err
		}
		if conn.Changes() == 0 {
			return fmt.Errorf("%s: %w", code, ErrUnknownGame)
		}
		return nil
	})
}

// AddPlayer inserts an active player in an existing game, active or not.
// The faction and email must be lowercase.
func (s *Store) AddPlayer(ctx context.Context, p Player) error {
	return s.tx(ctx, func(conn *sqlite.Conn) error {
		g, err := findGame(conn, p.GameCode)
		if err != nil {
			return err
		}
		err = sqlitex.Execute(conn, "INSERT INTO players (game_id, faction, email, name) VALUES (?, ?, ?, ?);", &sqlitex.ExecOptions{
			Args: []any{g.ID, p.Faction, p.Email, p.Name},
		})
		if isUnique(err) {
			return fmt.Errorf("%s %s: %w", p.GameCode, p.Faction, ErrPlayerExists)
		}
		return err
	})
}

const playerColumns = `p.id, g.code, p.faction, p.email, p.name, p.is_active`

func scanPlayer(stmt *sqlite.Stmt) Player {
	return Player{
		ID: stmt.ColumnInt64(0), GameCode: stmt.ColumnText(1), Faction: stmt.ColumnText(2),
		Email: stmt.ColumnText(3), Name: stmt.ColumnText(4), Active: stmt.ColumnBool(5),
	}
}

// Players returns a game's players, in faction order.
func (s *Store) Players(ctx context.Context, gameCode string) ([]Player, error) {
	var players []Player
	err := s.with(ctx, func(conn *sqlite.Conn) error {
		g, err := findGame(conn, gameCode)
		if err != nil {
			return err
		}
		return sqlitex.Execute(conn, "SELECT "+playerColumns+" FROM players p JOIN games g ON g.id = p.game_id WHERE p.game_id = ? ORDER BY p.faction;", &sqlitex.ExecOptions{
			Args: []any{g.ID},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				players = append(players, scanPlayer(stmt))
				return nil
			},
		})
	})
	return players, err
}

// Player returns a game's player with the faction.
func (s *Store) Player(ctx context.Context, gameCode, faction string) (Player, error) {
	var p Player
	err := s.with(ctx, func(conn *sqlite.Conn) error {
		var err error
		p, err = findPlayer(conn, gameCode, faction)
		return err
	})
	return p, err
}

func findPlayer(conn *sqlite.Conn, gameCode, faction string) (Player, error) {
	g, err := findGame(conn, gameCode)
	if err != nil {
		return Player{}, err
	}
	var p Player
	found := false
	err = sqlitex.Execute(conn, "SELECT "+playerColumns+" FROM players p JOIN games g ON g.id = p.game_id WHERE p.game_id = ? AND p.faction = ?;", &sqlitex.ExecOptions{
		Args: []any{g.ID, faction},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			p, found = scanPlayer(stmt), true
			return nil
		},
	})
	if err == nil && !found {
		err = fmt.Errorf("%s %s: %w", gameCode, faction, ErrUnknownPlayer)
	}
	return p, err
}

// UpdatePlayer changes the given fields of a player, active or not.
func (s *Store) UpdatePlayer(ctx context.Context, gameCode, faction string, c PlayerChange) error {
	return s.tx(ctx, func(conn *sqlite.Conn) error {
		p, err := findPlayer(conn, gameCode, faction)
		if err != nil {
			return err
		}
		if c.Faction != nil {
			p.Faction = *c.Faction
		}
		if c.Email != nil {
			p.Email = *c.Email
		}
		if c.Name != nil {
			p.Name = *c.Name
		}
		err = sqlitex.Execute(conn, "UPDATE players SET faction = ?, email = ?, name = ? WHERE id = ?;", &sqlitex.ExecOptions{
			Args: []any{p.Faction, p.Email, p.Name, p.ID},
		})
		if isUnique(err) {
			return fmt.Errorf("%s %s: %w", gameCode, p.Faction, ErrPlayerExists)
		}
		return err
	})
}

// SetPlayerActive sets whether a player is active. Repeating it succeeds.
func (s *Store) SetPlayerActive(ctx context.Context, gameCode, faction string, active bool) error {
	return s.tx(ctx, func(conn *sqlite.Conn) error {
		p, err := findPlayer(conn, gameCode, faction)
		if err != nil {
			return err
		}
		return sqlitex.Execute(conn, "UPDATE players SET is_active = ? WHERE id = ?;", &sqlitex.ExecOptions{Args: []any{active, p.ID}})
	})
}

// DeletePlayer deletes a player that nothing refers to. Nothing refers to
// players yet; once turns or orders do, deleting one of theirs fails with
// ErrPlayerInUse.
func (s *Store) DeletePlayer(ctx context.Context, gameCode, faction string) error {
	return s.tx(ctx, func(conn *sqlite.Conn) error {
		p, err := findPlayer(conn, gameCode, faction)
		if err != nil {
			return err
		}
		err = sqlitex.Execute(conn, "DELETE FROM players WHERE id = ?;", &sqlitex.ExecOptions{Args: []any{p.ID}})
		if isForeignKey(err) {
			return fmt.Errorf("%s %s: %w", gameCode, faction, ErrPlayerInUse)
		}
		return err
	})
}
