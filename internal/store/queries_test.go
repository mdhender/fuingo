// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// Games used throughout the tests: two in one system, and one in another
// system with the same code as the first.
var (
	game01      = GameRef{System: "system01", Code: "game01"}
	game02      = GameRef{System: "system01", Code: "game02"}
	otherGame01 = GameRef{System: "system02", Code: "game01"}
)

// seed inserts both systems and the three games, with rows added.
func seed(t *testing.T, s *Store, rows Rows) {
	t.Helper()
	rows.Systems = []string{"system01", "system02"}
	rows.Games = []GameRef{game01, game02, otherGame01}
	if err := s.CreateRows(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
}

func openMemory(t *testing.T) *Store {
	t.Helper()
	s, err := OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// isForeignKeyError reports a foreign key violation.
func isForeignKeyError(err error) bool {
	return sqlite.ErrCode(err) == sqlite.ResultConstraintForeignKey || sqlite.ErrCode(err) == sqlite.ResultConstraintTrigger
}

func joined(emails []string) string {
	return strings.Join(emails, ",")
}

func TestCreateSystem(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	id, err := s.CreateSystem(t.Context(), "olympia")
	if err != nil || id <= 0 {
		t.Fatalf("CreateSystem = %d, %v", id, err)
	}
	if _, err := s.CreateSystem(t.Context(), "olympia"); !errors.Is(err, ErrSystemExists) {
		t.Errorf("CreateSystem with an existing code = %v, want ErrSystemExists", err)
	}
	if _, err := s.CreateSystem(t.Context(), "Atlantis"); !errors.Is(err, ErrCodeCase) {
		t.Errorf("CreateSystem with an uppercase code = %v, want ErrCodeCase", err)
	}
	if got, err := s.Systems(t.Context()); err != nil || joined(got) != "olympia" {
		t.Errorf("Systems = %v, %v; want only olympia", got, err)
	}
}

func TestCreateGame(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	for _, system := range []string{"system01", "system02"} {
		if _, err := s.CreateSystem(t.Context(), system); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.CreateGame(t.Context(), game01)
	if err != nil {
		t.Fatalf("CreateGame: %v", err)
	}
	if got, ok, err := s.GameID(t.Context(), game01); err != nil || !ok || got != id {
		t.Errorf("GameID = %d, %v, %v; want %d, true, nil", got, ok, err, id)
	}
	// The same code in another system is a different game.
	other, err := s.CreateGame(t.Context(), otherGame01)
	if err != nil || other == id {
		t.Errorf("CreateGame of the same code in another system = %d, %v; want a new ID", other, err)
	}
	if got, ok, err := s.GameID(t.Context(), otherGame01); err != nil || !ok || got != other {
		t.Errorf("GameID in the other system = %d, %v, %v; want %d", got, ok, err, other)
	}
	for _, tc := range []struct {
		game GameRef
		want error
	}{
		{game01, ErrGameExists},
		{GameRef{System: "system01", Code: "Game03"}, ErrCodeCase},
		{GameRef{System: "system99", Code: "game01"}, ErrUnknownSystem},
	} {
		if _, err := s.CreateGame(t.Context(), tc.game); !errors.Is(err, tc.want) {
			t.Errorf("CreateGame(%s) = %v, want %v", tc.game, err, tc.want)
		}
	}
	if _, ok, _ := s.GameID(t.Context(), GameRef{System: "system02", Code: "game02"}); ok {
		t.Error("GameID found a game under the wrong system")
	}
	if games, _ := s.Games(t.Context()); len(games) != 2 {
		t.Errorf("games = %v, want two", games)
	}
}

func TestSetGameActive(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{Players: []Member{{game01, "p@example.com"}, {otherGame01, "p@example.com"}}})
	for range 2 { // disabling twice succeeds
		if err := s.SetGameActive(t.Context(), game01, false); err != nil {
			t.Fatalf("SetGameActive(false): %v", err)
		}
	}
	if got, _ := s.Players(t.Context(), game01); len(got) != 0 {
		t.Errorf("players of an inactive game = %v, want none", got)
	}
	if got, _ := s.Players(t.Context(), otherGame01); joined(got) != "p@example.com" {
		t.Errorf("players of the same code in another system = %v, want p@example.com", got)
	}
	if err := s.SetGameActive(t.Context(), game01, true); err != nil {
		t.Fatalf("SetGameActive(true): %v", err)
	}
	if got, _ := s.Players(t.Context(), game01); joined(got) != "p@example.com" {
		t.Errorf("players of a re-enabled game = %v, want p@example.com", got)
	}
	if err := s.SetGameActive(t.Context(), GameRef{System: "system02", Code: "game02"}, false); !errors.Is(err, ErrUnknownGame) {
		t.Errorf("SetGameActive of a missing game = %v, want ErrUnknownGame", err)
	}
}

func TestCreateAccount(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	id, err := s.CreateAccount(t.Context(), NewAccount{Email: "a@example.com", HashedSecret: []byte("hash-a")})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	a, ok, err := s.FindAccount(t.Context(), "a@example.com")
	if err != nil || !ok {
		t.Fatalf("FindAccount = %v, %v", ok, err)
	}
	if a.ID != id || a.Email != "a@example.com" || string(a.HashedSecret) != "hash-a" {
		t.Errorf("FindAccount = %+v, want ID %d and hash-a", a, id)
	}
	if _, err := s.CreateAccount(t.Context(), NewAccount{Email: "a@example.com", HashedSecret: []byte("hash-b")}); !errors.Is(err, ErrAccountExists) {
		t.Errorf("CreateAccount with an existing email = %v, want ErrAccountExists", err)
	}
	if a, _, _ := s.FindAccount(t.Context(), "a@example.com"); string(a.HashedSecret) != "hash-a" {
		t.Errorf("a duplicate CreateAccount changed the password hash to %q", a.HashedSecret)
	}
	if _, err := s.CreateAccount(t.Context(), NewAccount{Email: "B@example.com", HashedSecret: []byte("hash-b")}); !errors.Is(err, ErrEmailCase) {
		t.Errorf("CreateAccount with an uppercase email = %v, want ErrEmailCase", err)
	}
	if _, ok, _ := s.FindAccount(t.Context(), "missing@example.com"); ok {
		t.Error("FindAccount found a missing account")
	}
}

func TestCreateRowsIsAtomic(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	err := s.CreateRows(t.Context(), Rows{
		Systems:  []string{"system01"},
		Games:    []GameRef{game01},
		Accounts: []NewAccount{{Email: "a@example.com", HashedSecret: []byte("x")}, {Email: "a@example.com", HashedSecret: []byte("y")}},
	})
	if !errors.Is(err, ErrAccountExists) {
		t.Fatalf("CreateRows with a duplicate account = %v, want ErrAccountExists", err)
	}
	if systems, _ := s.Systems(t.Context()); len(systems) != 0 {
		t.Errorf("systems = %v, want none", systems)
	}
	if games, _ := s.Games(t.Context()); len(games) != 0 {
		t.Errorf("games = %v, want none", games)
	}
	if _, ok, _ := s.FindAccount(t.Context(), "a@example.com"); ok {
		t.Error("CreateRows kept an account after failing")
	}
}

func TestAddManager(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{
		Accounts: []NewAccount{{Email: "a@example.com", HashedSecret: []byte("x")}, {Email: "b@example.com", HashedSecret: []byte("y")}},
		Managers: []Member{{game01, "b@example.com"}},
	})
	if err := s.AddManager(t.Context(), Member{game01, "a@example.com"}); err != nil {
		t.Fatalf("AddManager: %v", err)
	}
	if err := s.AddManager(t.Context(), Member{game02, "a@example.com"}); err != nil {
		t.Fatalf("AddManager to a second game: %v", err)
	}
	for _, tc := range []struct {
		m    Member
		want error
	}{
		{Member{game01, "a@example.com"}, ErrManagerExists},
		{Member{GameRef{System: "system01", Code: "game99"}, "a@example.com"}, ErrUnknownGame},
		{Member{GameRef{System: "system99", Code: "game01"}, "a@example.com"}, ErrUnknownGame},
		{Member{game01, "missing@example.com"}, ErrUnknownAccount},
	} {
		if err := s.AddManager(t.Context(), tc.m); !errors.Is(err, tc.want) {
			t.Errorf("AddManager(%+v) = %v, want %v", tc.m, err, tc.want)
		}
	}
	for game, want := range map[GameRef]string{
		game01:      "a@example.com,b@example.com",
		game02:      "a@example.com",
		otherGame01: "", // managing system01/game01 is not managing system02/game01
	} {
		if got, err := s.Managers(t.Context(), game); err != nil || joined(got) != want {
			t.Errorf("Managers(%s) = %v, %v; want %s", game, got, err, want)
		}
	}
}

func TestReferencedRowsCannotBeDeleted(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{
		Accounts: []NewAccount{{Email: "a@example.com", HashedSecret: []byte("x")}},
		Players:  []Member{{game02, "p@example.com"}},
		Managers: []Member{{game01, "a@example.com"}},
	})
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	for _, stmt := range []string{"DELETE FROM systems;", "DELETE FROM games;", "DELETE FROM accounts;"} {
		if err := sqlitex.ExecuteTransient(conn, stmt, nil); !isForeignKeyError(err) {
			t.Errorf("%s = %v, want a foreign key error", stmt, err)
		}
	}
}

func TestSetManagerActive(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{
		Accounts: []NewAccount{{Email: "a@example.com", HashedSecret: []byte("x")}, {Email: "b@example.com", HashedSecret: []byte("y")}},
		Managers: []Member{{game01, "a@example.com"}, {game01, "b@example.com"}, {game02, "a@example.com"}, {otherGame01, "a@example.com"}},
	})
	managers := func(game GameRef) string {
		t.Helper()
		got, err := s.Managers(t.Context(), game)
		if err != nil {
			t.Fatal(err)
		}
		return joined(got)
	}
	for range 2 { // disabling twice succeeds
		if err := s.SetManagerActive(t.Context(), Member{game01, "a@example.com"}, false); err != nil {
			t.Fatalf("SetManagerActive(false): %v", err)
		}
	}
	if got := managers(game01); got != "b@example.com" {
		t.Errorf("managers of game01 = %s, want only b@example.com", got)
	}
	for _, game := range []GameRef{game02, otherGame01} {
		if got := managers(game); got != "a@example.com" {
			t.Errorf("managers of %s = %s, want a@example.com", game, got)
		}
	}
	if err := s.AddManager(t.Context(), Member{game01, "a@example.com"}); !errors.Is(err, ErrManagerExists) {
		t.Errorf("AddManager of an inactive manager = %v, want ErrManagerExists", err)
	}
	if err := s.SetManagerActive(t.Context(), Member{game01, "a@example.com"}, true); err != nil {
		t.Fatalf("SetManagerActive(true): %v", err)
	}
	if got := managers(game01); got != "a@example.com,b@example.com" {
		t.Errorf("managers of game01 = %s, want both", got)
	}
	for _, tc := range []struct {
		m    Member
		want error
	}{
		{Member{game02, "b@example.com"}, ErrUnknownManager},
		{Member{game01, "missing@example.com"}, ErrUnknownManager},
		{Member{GameRef{System: "system01", Code: "game99"}, "a@example.com"}, ErrUnknownGame},
	} {
		if err := s.SetManagerActive(t.Context(), tc.m, false); !errors.Is(err, tc.want) {
			t.Errorf("SetManagerActive(%+v) = %v, want %v", tc.m, err, tc.want)
		}
	}
}

func TestSetAccountActive(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{
		Accounts: []NewAccount{{Email: "a@example.com", HashedSecret: []byte("x")}, {Email: "b@example.com", HashedSecret: []byte("y")}},
		Players:  []Member{{game02, "a@example.com"}},
		Managers: []Member{{game01, "a@example.com"}, {game01, "b@example.com"}},
	})
	check := func(wantActive bool, wantManagers, wantPlayers string) {
		t.Helper()
		a, ok, err := s.FindAccount(t.Context(), "a@example.com")
		if err != nil || !ok || a.Active != wantActive {
			t.Errorf("FindAccount = %+v, %v, %v; want Active %v", a, ok, err, wantActive)
		}
		if got, err := s.Managers(t.Context(), game01); err != nil || joined(got) != wantManagers {
			t.Errorf("Managers = %v, %v; want %s", got, err, wantManagers)
		}
		if got, err := s.Players(t.Context(), game02); err != nil || joined(got) != wantPlayers {
			t.Errorf("Players = %v, %v; want %s", got, err, wantPlayers)
		}
	}
	check(true, "a@example.com,b@example.com", "a@example.com")
	for range 2 { // disabling twice succeeds
		if err := s.SetAccountActive(t.Context(), "a@example.com", false); err != nil {
			t.Fatalf("SetAccountActive(false): %v", err)
		}
	}
	check(false, "b@example.com", "")
	if err := s.SetAccountActive(t.Context(), "a@example.com", true); err != nil {
		t.Fatalf("SetAccountActive(true): %v", err)
	}
	check(true, "a@example.com,b@example.com", "a@example.com")
	if err := s.SetAccountActive(t.Context(), "missing@example.com", false); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("SetAccountActive of a missing account = %v, want ErrUnknownAccount", err)
	}
}

// TestPlayersHaveAccounts checks that a player uses the account with its
// email, creating one without a secret if needed, and that one account is a
// separate player in each game.
func TestPlayersHaveAccounts(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{
		Accounts: []NewAccount{{Email: "a@example.com", HashedSecret: []byte("x")}},
		Players:  []Member{{game01, "a@example.com"}, {game01, "b@example.com"}},
	})
	if a, _, _ := s.FindAccount(t.Context(), "a@example.com"); string(a.HashedSecret) != "x" {
		t.Errorf("player a@example.com changed its account's secret to %q", a.HashedSecret)
	}
	b, ok, err := s.FindAccount(t.Context(), "b@example.com")
	if err != nil || !ok || b.HashedSecret != nil || !b.Active {
		t.Errorf("FindAccount(b) = %+v, %v, %v; want an active account without a secret", b, ok, err)
	}
	if _, err := s.CreateAccount(t.Context(), NewAccount{Email: "b@example.com", HashedSecret: []byte("y")}); !errors.Is(err, ErrAccountExists) {
		t.Errorf("CreateAccount for a player's email = %v, want ErrAccountExists", err)
	}
	first, err := s.AddPlayer(t.Context(), Member{game02, "b@example.com"})
	if err != nil {
		t.Fatalf("AddPlayer in a second game: %v", err)
	}
	second, err := s.AddPlayer(t.Context(), Member{otherGame01, "b@example.com"})
	if err != nil || second == first {
		t.Fatalf("AddPlayer in another system = %d, %v; want a new ID after %d", second, err, first)
	}
	for _, tc := range []struct {
		m    Member
		want error
	}{
		{Member{game02, "b@example.com"}, ErrPlayerExists},
		{Member{GameRef{System: "system02", Code: "game02"}, "b@example.com"}, ErrUnknownGame},
	} {
		if _, err := s.AddPlayer(t.Context(), tc.m); !errors.Is(err, tc.want) {
			t.Errorf("AddPlayer(%+v) = %v, want %v", tc.m, err, tc.want)
		}
	}
	if got, _ := s.Players(t.Context(), game01); joined(got) != "a@example.com,b@example.com" {
		t.Errorf("Players(game01) = %v", got)
	}
}

func TestSetPlayerActive(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{Players: []Member{{game01, "p@example.com"}, {game02, "p@example.com"}, {otherGame01, "p@example.com"}}})
	for range 2 { // disabling twice succeeds
		if err := s.SetPlayerActive(t.Context(), Member{game01, "p@example.com"}, false); err != nil {
			t.Fatalf("SetPlayerActive(false): %v", err)
		}
	}
	for game, want := range map[GameRef]string{game01: "", game02: "p@example.com", otherGame01: "p@example.com"} {
		if got, _ := s.Players(t.Context(), game); joined(got) != want {
			t.Errorf("Players(%s) = %v, want %q", game, got, want)
		}
	}
	if _, err := s.AddPlayer(t.Context(), Member{game01, "p@example.com"}); !errors.Is(err, ErrPlayerExists) {
		t.Errorf("AddPlayer of an inactive player = %v, want ErrPlayerExists", err)
	}
	if err := s.SetPlayerActive(t.Context(), Member{game01, "p@example.com"}, true); err != nil {
		t.Fatalf("SetPlayerActive(true): %v", err)
	}
	if got, _ := s.Players(t.Context(), game01); joined(got) != "p@example.com" {
		t.Errorf("Players(game01) after enabling = %v", got)
	}
	if err := s.SetPlayerActive(t.Context(), Member{game01, "missing@example.com"}, false); !errors.Is(err, ErrUnknownPlayer) {
		t.Errorf("SetPlayerActive of a missing player = %v, want ErrUnknownPlayer", err)
	}
}

func TestSetAccountSecret(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{
		Accounts: []NewAccount{{Email: "a@example.com", HashedSecret: []byte("x")}},
		Players:  []Member{{game01, "p@example.com"}},
	})
	for _, email := range []string{"a@example.com", "p@example.com"} {
		if err := s.SetAccountSecret(t.Context(), email, []byte("new")); err != nil {
			t.Fatalf("SetAccountSecret(%s): %v", email, err)
		}
		if a, _, _ := s.FindAccount(t.Context(), email); string(a.HashedSecret) != "new" {
			t.Errorf("%s: secret = %q, want new", email, a.HashedSecret)
		}
	}
	if got, _ := s.Players(t.Context(), game01); joined(got) != "p@example.com" {
		t.Errorf("setting the secret changed the players to %v", got)
	}
	if err := s.SetAccountSecret(t.Context(), "missing@example.com", []byte("new")); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("SetAccountSecret of a missing account = %v, want ErrUnknownAccount", err)
	}
}

// TestMigrateFromVersion2 migrates a database made under the header-line
// design: its games move into the system "legacy", its uploaders become
// players, every row keeps its ID, and no ID is ever reused, even one whose
// row was deleted before the migration.
func TestMigrateFromVersion2(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "v2.db")
	old := schema
	old.Migrations = migrations[:2]
	db, err := old.Create(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Pool.Take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = sqlitex.ExecuteScript(conn, `
INSERT INTO games (code, is_active) VALUES ('game01', 1), ('game02', 0), ('gone', 1);
DELETE FROM games WHERE code = 'gone';
INSERT INTO accounts (email, hashed_secret) VALUES ('gm@example.com', 'hash'), ('p@example.com', NULL), ('q@example.com', NULL);
INSERT INTO game_uploaders (game_id, account_id, hashed_credentials, is_active) VALUES (1, 2, x'01', 1), (2, 2, x'02', 0), (1, 3, x'03', 1);
DELETE FROM game_uploaders WHERE id = 3;
INSERT INTO game_managers (game_id, account_id, is_active) VALUES (1, 1, 1), (2, 1, 0), (2, 3, 1);
DELETE FROM game_managers WHERE id = 3;
INSERT INTO upload_blobs (content_hash, contents, created_at) VALUES ('0000000000000000000000000000000000000000000000000000000000000000', 'orders', 'x');
INSERT INTO uploads (uploader_id, email, turn_no, content_hash, uploaded_at) VALUES (1, 'p@example.com', '1', '0000000000000000000000000000000000000000000000000000000000000000', 'x');
`, nil)
	db.Pool.Put(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(t.Context(), path); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	legacy01, legacy02 := GameRef{System: "legacy", Code: "game01"}, GameRef{System: "legacy", Code: "game02"}
	if got, _ := s.Systems(t.Context()); joined(got) != "legacy" {
		t.Errorf("systems = %v, want legacy", got)
	}
	for game, want := range map[GameRef]int64{legacy01: 1, legacy02: 2} {
		if id, ok, err := s.GameID(t.Context(), game); err != nil || !ok || id != want {
			t.Errorf("GameID(%s) = %d, %v, %v; want %d", game, id, ok, err, want)
		}
	}
	if got, _ := s.Players(t.Context(), legacy01); joined(got) != "p@example.com" {
		t.Errorf("players of game01 = %v, want p@example.com", got)
	}
	if got, _ := s.Players(t.Context(), legacy02); len(got) != 0 {
		t.Errorf("players of the inactive game02 = %v, want none", got)
	}
	if got, _ := s.Managers(t.Context(), legacy01); joined(got) != "gm@example.com" {
		t.Errorf("managers of game01 = %v, want gm@example.com", got)
	}
	if got, _ := s.Managers(t.Context(), legacy02); len(got) != 0 {
		t.Errorf("managers of game02 = %v, want none: its manager was inactive", got)
	}
	players, err := s.GamePlayers(t.Context(), 2)
	if err != nil || len(players) != 1 || players[0].ID != 2 || players[0].Active {
		t.Errorf("players of game02 = %+v, %v; want the inactive player 2", players, err)
	}

	// New rows continue each sequence past the deleted rows.
	if id, err := s.CreateGame(t.Context(), GameRef{System: "legacy", Code: "game03"}); err != nil || id != 4 {
		t.Errorf("next game ID = %d, %v; want 4", id, err)
	}
	if id, err := s.AddPlayer(t.Context(), Member{legacy01, "q@example.com"}); err != nil || id != 4 {
		t.Errorf("next player ID = %d, %v; want 4", id, err)
	}
	if err := s.AddManager(t.Context(), Member{legacy01, "q@example.com"}); err != nil {
		t.Fatal(err)
	}
	conn, err = s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	if id, err := sqlitex.ResultInt64(conn.Prep("SELECT max(id) FROM game_managers;")); err != nil || id != 4 {
		t.Errorf("next manager ID = %d, %v; want 4", id, err)
	}
	if err := sqlitex.ExecuteTransient(conn, "PRAGMA foreign_key_check;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			t.Errorf("foreign key violation in %s", stmt.ColumnText(0))
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	// The previous design's uploads are dropped; a later migration makes the
	// new, empty uploads table.
	if n, err := sqlitex.ResultInt(conn.Prep("SELECT count(*) FROM uploads;")); err != nil || n != 0 {
		t.Errorf("uploads after migrating = %d, %v; want none", n, err)
	}
	for _, table := range []string{"upload_blobs", "game_uploaders", "games_v2", "game_managers_v3"} {
		if n, err := sqlitex.ResultInt(conn.Prep("SELECT count(*) FROM sqlite_schema WHERE name = '" + table + "';")); err != nil || n != 0 {
			t.Errorf("table %s: count = %d, %v; want it gone", table, n, err)
		}
	}
}

// TestMigrateEmptyVersion2 migrates a database with no games: it gets no
// legacy system.
func TestMigrateEmptyVersion2(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "v2.db")
	old := schema
	old.Migrations = migrations[:2]
	db, err := old.Create(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), path); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if got, _ := s.Systems(t.Context()); len(got) != 0 {
		t.Errorf("systems = %v, want none", got)
	}
}
