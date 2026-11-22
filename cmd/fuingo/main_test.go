// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// clearEnv keeps the developer's environment out of the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FUINGO_ENV", "FUINGO_DB", "FUINGO_SCHEMA", "FUINGO_GENERATE_PASSWORD", "FUINGO_MANAGER", "FUINGO_PLAYER", "FUINGO_SYSTEM", "FUINGO_CODE"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func runCommand(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	return runWithInput(t, "", args...)
}

// runWithInput runs the command with input on standard input.
func runWithInput(t *testing.T, input string, args ...string) (stdout string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = run(t.Context(), args, strings.NewReader(input), &out, &errOut)
	if err != nil {
		return out.String() + errOut.String(), err
	}
	return out.String(), nil
}

// createDatabase makes a current database and returns its path.
func createDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fuingo.db")
	s, err := store.Create(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// setSchemaVersion overwrites the database's schema version.
func setSchemaVersion(t *testing.T, path string, version int) {
	t.Helper()
	conn, err := sqlite.OpenConn(path, sqlite.OpenReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := sqlitex.ExecuteTransient(conn, "PRAGMA user_version = "+strconv.Itoa(version)+";", nil); err != nil {
		t.Fatal(err)
	}
}

func TestVersion(t *testing.T) {
	clearEnv(t)
	out, err := runCommand(t, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	if want := fuingo.Version().String() + "\n"; out != want {
		t.Errorf("version printed %q, want %q", out, want)
	}
}

func TestVersionIgnoresDatabase(t *testing.T) {
	clearEnv(t)
	missing := filepath.Join(t.TempDir(), "missing.db")
	out, err := runCommand(t, "--db", missing, "version")
	if err != nil {
		t.Fatalf("--db %s version: %v\n%s", missing, err, out)
	}
	if want := fuingo.Version().String() + "\n"; out != want {
		t.Errorf("version printed %q, want %q", out, want)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("version created %s", missing)
	}
}

func TestSchemaVersion(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	want := strconv.Itoa(store.CurrentSchemaVersion()) + "\n"
	for _, args := range [][]string{
		{"--env", "test", "--db", path, "version", "--schema"},
		{"--env", "test", "version", "--schema", "--db", path},
		{"version", "--schema", "--env", "test", "--db", path},
	} {
		out, err := runCommand(t, args...)
		if err != nil {
			t.Fatalf("%q: %v\n%s", args, err, out)
		}
		if out != want {
			t.Errorf("%q printed %q, want %q", args, out, want)
		}
	}
}

func TestSchemaVersionFromEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_ENV", "test")
	t.Setenv("FUINGO_DB", createDatabase(t))
	out, err := runCommand(t, "version", "--schema")
	if err != nil {
		t.Fatalf("version --schema: %v\n%s", err, out)
	}
	if want := strconv.Itoa(store.CurrentSchemaVersion()) + "\n"; out != want {
		t.Errorf("version --schema printed %q, want %q", out, want)
	}
}

func TestSchemaVersionRefusesOtherSchemaVersions(t *testing.T) {
	clearEnv(t)
	for _, tc := range []struct {
		version int
		want    error
	}{
		{store.CurrentSchemaVersion() - 1, store.ErrOutdatedSchema},
		{store.CurrentSchemaVersion() + 1, store.ErrNewerSchema},
	} {
		path := createDatabase(t)
		setSchemaVersion(t, path, tc.version)
		out, err := runCommand(t, "--env", "test", "--db", path, "version", "--schema")
		if !errors.Is(err, tc.want) {
			t.Errorf("schema version %d: err = %v, want %v\n%s", tc.version, err, tc.want, out)
		}
	}
}

func TestSchemaVersionRefusesBadPaths(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	foreign := filepath.Join(dir, "foreign.db")
	if err := os.WriteFile(foreign, []byte("not a database\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.db")
	for _, tc := range []struct {
		path string
		want error
	}{
		{"", ErrMissingDatabase},
		{":memory:", ErrMemoryDatabase},
		{missing, fs.ErrNotExist},
	} {
		out, err := runCommand(t, "--env", "test", "--db", tc.path, "version", "--schema")
		if !errors.Is(err, tc.want) {
			t.Errorf("--db %q: err = %v, want %v\n%s", tc.path, err, tc.want, out)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("version --schema created %s", missing)
	}
	if out, err := runCommand(t, "--env", "test", "--db", foreign, "version", "--schema"); err == nil {
		t.Errorf("version --schema accepted a file that is not a fuingo database:\n%s", out)
	}
}

func TestSchemaVersionRequiresEnvironment(t *testing.T) {
	clearEnv(t)
	out, err := runCommand(t, "--db", createDatabase(t), "version", "--schema")
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Errorf("version --schema without an environment: err = %v\n%s", err, out)
	}
}

// Games used throughout the tests.
var (
	game01 = store.GameRef{System: "system01", Code: "game01"}
	game02 = store.GameRef{System: "system01", Code: "game02"}
)

// open opens the database at path for the test.
func open(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// systems returns the codes of the systems in the database at path.
func systems(t *testing.T, path string) []string {
	t.Helper()
	got, err := open(t, path).Systems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// games returns the games in the database at path, as system/code.
func games(t *testing.T, path string) []string {
	t.Helper()
	refs, err := open(t, path).Games(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range refs {
		got = append(got, g.String())
	}
	return got
}

// managers returns the emails of the game's active managers.
func managers(t *testing.T, path string, game store.GameRef) []string {
	t.Helper()
	got, err := open(t, path).Managers(t.Context(), game)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// players returns the emails of the game's active players.
func players(t *testing.T, path string, game store.GameRef) []string {
	t.Helper()
	got, err := open(t, path).Players(t.Context(), game)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// mustRun runs the command and checks what it printed.
func mustRun(t *testing.T, want string, args ...string) {
	t.Helper()
	out, err := runCommand(t, args...)
	if err != nil {
		t.Fatalf("%q: %v\n%s", args, err, out)
	}
	if out != want {
		t.Errorf("%q printed %q, want %q", args, out, want)
	}
}

// createDatabaseWithGames makes a current database holding system01 and
// system02, game01 and game02 in system01, and the given accounts.
func createDatabaseWithGames(t *testing.T, emails ...string) string {
	t.Helper()
	path := createDatabase(t)
	rows := store.Rows{Systems: []string{"system01", "system02"}, Games: []store.GameRef{game01, game02}}
	for _, email := range emails {
		rows.Accounts = append(rows.Accounts, store.NewAccount{Email: email, HashedSecret: []byte("hash")})
	}
	if err := open(t, path).CreateRows(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	return path
}

// toggleFixture makes a database with game01 and game02 in system01, players
// catbird and spoonbill in game01, and account acct01 managing both games.
func toggleFixture(t *testing.T) string {
	t.Helper()
	path := createDatabaseWithGames(t, "acct01@example.com")
	if err := open(t, path).CreateRows(t.Context(), store.Rows{
		Players:  []store.Member{{Game: game01, Email: "catbird@example.com"}, {Game: game01, Email: "spoonbill@example.com"}},
		Managers: []store.Member{{Game: game01, Email: "acct01@example.com"}, {Game: game02, Email: "acct01@example.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSystemCreate(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	mustRun(t, "created system olympia (id 1)\n", "--env", "test", "--db", path, "system", "create", "Olympia")
	mustRun(t, "created system atlantis (id 2)\n", "--env", "test", "--db", path, "system", "create", "atlantis")
	if got := systems(t, path); !slices.Equal(got, []string{"olympia", "atlantis"}) {
		t.Errorf("systems = %v", got)
	}
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"OLYMPIA"}, store.ErrSystemExists},
		{[]string{}, ErrUsage},
		{[]string{"a", "b"}, ErrUsage},
	} {
		args := append([]string{"--env", "test", "--db", path, "system", "create"}, tc.args...)
		if out, err := runCommand(t, args...); !errors.Is(err, tc.want) {
			t.Errorf("%q: err = %v, want %v\n%s", args, err, tc.want, out)
		}
	}
	if got := systems(t, path); len(got) != 2 {
		t.Errorf("a refused command changed the systems to %v", got)
	}
}

// invalidCodes are refused as the code of a system or a game.
var invalidCodes = []string{
	"a b", " ab", "ab ", "a\tb", "a/b", "a\\b", "a<b", "a>b", "a&b", "a;b", "a'b", "a\"b", "a`b", "a.b", "a\x00b",
	strings.Repeat("a", 33), strings.Repeat("é", 33),
}

func TestCodes(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	// 32 characters, counted in characters and not bytes; letters of any
	// script, digits, hyphens, and underscores.
	for _, code := range []string{strings.Repeat("a", 32), strings.Repeat("é", 32), "été-2_ω", "Ωmega"} {
		lower := strings.ToLower(code)
		out, err := runCommand(t, "--env", "test", "--db", path, "system", "create", code)
		if err != nil || !strings.HasPrefix(out, "created system "+lower+" ") {
			t.Errorf("system create %q: %q, %v", code, out, err)
		}
		out, err = runCommand(t, "--env", "test", "--db", path, "game", "create", "--system", code, code)
		if err != nil || !strings.HasPrefix(out, "created game "+lower+"/"+lower+" ") {
			t.Errorf("game create %q: %q, %v", code, out, err)
		}
	}
	mustRun(t, "created system system01 (id 5)\n", "--env", "test", "--db", path, "system", "create", "system01")
	before := games(t, path)
	for _, code := range invalidCodes {
		if out, err := runCommand(t, "--env", "test", "--db", path, "system", "create", code); !errors.Is(err, names.ErrCode) {
			t.Errorf("system create %q: err = %v, want ErrCode\n%s", code, err, out)
		}
		if out, err := runCommand(t, "--env", "test", "--db", path, "game", "create", "--system", "system01", code); !errors.Is(err, names.ErrCode) {
			t.Errorf("game create %q: err = %v, want ErrCode\n%s", code, err, out)
		}
		if out, err := runCommand(t, "--env", "test", "--db", path, "game", "create", "--system", code, "game01"); !errors.Is(err, names.ErrCode) {
			t.Errorf("game create --system %q: err = %v, want ErrCode\n%s", code, err, out)
		}
	}
	if got := systems(t, path); len(got) != 5 {
		t.Errorf("a refused command changed the systems to %v", got)
	}
	if got := games(t, path); !slices.Equal(got, before) {
		t.Errorf("a refused command changed the games to %v", got)
	}
}

func TestGameCreate(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	mustRun(t, "created system olympia (id 1)\n", "--env", "test", "--db", path, "system", "create", "olympia")
	mustRun(t, "created system atlantis (id 2)\n", "--env", "test", "--db", path, "system", "create", "atlantis")
	mustRun(t, "created game olympia/g3 (id 1)\n", "--env", "test", "--db", path, "game", "create", "--system", "Olympia", "G3")
	// A code is unique within its system, not across systems.
	mustRun(t, "created game atlantis/g3 (id 2)\n", "--env", "test", "--db", path, "game", "create", "--system", "atlantis", "g3")
	mustRun(t, "created game olympia/g4 (id 3)\n", "--db", path, "--env", "test", "game", "create", "--system", "olympia", "g4")
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"--system", "olympia", "G3"}, store.ErrGameExists},
		{[]string{"--system", "nowhere", "g3"}, store.ErrUnknownSystem},
		{[]string{"g5"}, ErrMissingSystem},
		{[]string{"--system", "olympia"}, ErrUsage},
		{[]string{"--system", "olympia", "g5", "g6"}, ErrUsage},
	} {
		args := append([]string{"--env", "test", "--db", path, "game", "create"}, tc.args...)
		if out, err := runCommand(t, args...); !errors.Is(err, tc.want) {
			t.Errorf("%q: err = %v, want %v\n%s", args, err, tc.want, out)
		}
	}
	if got := games(t, path); !slices.Equal(got, []string{"olympia/g3", "atlantis/g3", "olympia/g4"}) {
		t.Errorf("games = %v", got)
	}
	// A new game is active: a player added to it is an active player.
	mustRun(t, "added player wren@example.com to game olympia/g3 (id 1)\n", "--env", "test", "--db", path, "game", "add", "--player", "wren@example.com", "--system", "olympia", "--code", "g3")
	if got := players(t, path, store.GameRef{System: "olympia", Code: "g3"}); len(got) != 1 {
		t.Errorf("players of a new game = %v", got)
	}
}

func TestCommandsRefuseOtherSchemaVersions(t *testing.T) {
	clearEnv(t)
	for _, version := range []int{store.CurrentSchemaVersion() - 1, store.CurrentSchemaVersion() + 1} {
		path := createDatabase(t)
		setSchemaVersion(t, path, version)
		for _, args := range [][]string{
			{"system", "create", "system01"},
			{"game", "create", "--system", "system01", "game01"},
		} {
			if out, err := runCommand(t, append([]string{"--env", "test", "--db", path}, args...)...); err == nil {
				t.Errorf("schema %d: %q succeeded\n%s", version, args, out)
			}
		}
	}
}

func TestGameAddManager(t *testing.T) {
	clearEnv(t)
	path := createDatabaseWithGames(t, "acct01@example.com", "acct02@example.com")
	base := []string{"--env", "test", "--db", path, "game", "add"}
	mustRun(t, "added manager acct01@example.com to game system01/game01\n", append(base, "--manager", "ACCT01@example.com", "--system", "SYSTEM01", "--code", "GAME01")...)
	mustRun(t, "added manager acct02@example.com to game system01/game01\n", append(base, "--code", "game01", "--system", "system01", "--manager", "acct02@example.com")...)
	mustRun(t, "added manager acct01@example.com to game system01/game02\n", append(base, "--manager", "acct01@example.com", "--system", "system01", "--code", "game02")...)
	if got := managers(t, path, game01); !slices.Equal(got, []string{"acct01@example.com", "acct02@example.com"}) {
		t.Errorf("managers of game01 = %v", got)
	}
	if got := managers(t, path, game02); !slices.Equal(got, []string{"acct01@example.com"}) {
		t.Errorf("managers of game02 = %v", got)
	}
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"--manager", "acct01@example.com", "--system", "system01", "--code", "game01"}, store.ErrManagerExists},
		{[]string{"--manager", "acct01@example.com", "--system", "system01", "--code", "game99"}, store.ErrUnknownGame},
		{[]string{"--manager", "acct01@example.com", "--system", "system02", "--code", "game01"}, store.ErrUnknownGame},
		{[]string{"--manager", "missing@example.com", "--system", "system01", "--code", "game01"}, store.ErrUnknownAccount},
		{[]string{"--manager", "acct 01@example.com", "--system", "system01", "--code", "game01"}, names.ErrEmail},
		{[]string{"--manager", "acct01@example.com", "--system", "system01", "--code", "game 01"}, names.ErrCode},
		{[]string{"--manager", "acct01@example.com", "--system", "system01"}, ErrMissingCode},
		{[]string{"--manager", "acct01@example.com", "--code", "game01"}, ErrMissingSystem},
		{[]string{"--system", "system01", "--code", "game01"}, ErrAddWhat},
		{[]string{"--manager", "acct01@example.com", "--player", "p@example.com", "--system", "system01", "--code", "game01"}, ErrAddWhat},
		{[]string{"--manager", "acct01@example.com", "--system", "system01", "--code", "game01", "extra"}, ErrUsage},
	} {
		if out, err := runCommand(t, append(base, tc.args...)...); !errors.Is(err, tc.want) {
			t.Errorf("game add %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	// A manager must already have an account; none was made.
	if _, ok := account(t, path, "missing@example.com"); ok {
		t.Error("game add --manager made an account")
	}
	if got := managers(t, path, game01); len(got) != 2 {
		t.Errorf("a refused command changed the managers to %v", got)
	}
}

func TestGameAddPlayer(t *testing.T) {
	clearEnv(t)
	path := createDatabaseWithGames(t, "acct01@example.com")
	base := []string{"--env", "test", "--db", path, "game", "add"}
	mustRun(t, "added player catbird@example.com to game system01/game01 (id 1)\n", append(base, "--player", "Catbird@Example.com", "--system", "System01", "--code", "Game01")...)
	// The account is new and has no password.
	if a, ok := account(t, path, "catbird@example.com"); !ok || a.HashedSecret != nil || !a.Active {
		t.Errorf("account = %+v, %v; want an active one without a password", a, ok)
	}
	// The same account is a different player in another game.
	mustRun(t, "added player catbird@example.com to game system01/game02 (id 2)\n", append(base, "--player", "catbird@example.com", "--system", "system01", "--code", "game02")...)
	// An account with a password keeps it.
	mustRun(t, "added player acct01@example.com to game system01/game01 (id 3)\n", append(base, "--player", "acct01@example.com", "--system", "system01", "--code", "game01")...)
	if a, _ := account(t, path, "acct01@example.com"); string(a.HashedSecret) != "hash" {
		t.Errorf("game add --player changed the password hash to %q", a.HashedSecret)
	}
	if got := players(t, path, game01); !slices.Equal(got, []string{"acct01@example.com", "catbird@example.com"}) {
		t.Errorf("players of game01 = %v", got)
	}
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"--player", "CATBIRD@example.com", "--system", "system01", "--code", "game01"}, store.ErrPlayerExists},
		{[]string{"--player", "wren@example.com", "--system", "system01", "--code", "game99"}, store.ErrUnknownGame},
		{[]string{"--player", "wren@example.com", "--system", "system02", "--code", "game01"}, store.ErrUnknownGame},
		{[]string{"--player", "wr en@example.com", "--system", "system01", "--code", "game01"}, names.ErrEmail},
		{[]string{"--player", "wren@example.com", "--system", "system01"}, ErrMissingCode},
		{[]string{"--player", "wren@example.com", "--code", "game01"}, ErrMissingSystem},
		{[]string{"--player", "wren@example.com", "--system", "system01", "--code", "game01", "extra"}, ErrUsage},
	} {
		if out, err := runCommand(t, append(base, tc.args...)...); !errors.Is(err, tc.want) {
			t.Errorf("game add %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	if _, ok := account(t, path, "wren@example.com"); ok {
		t.Error("a refused game add --player made an account")
	}
	// The previous design's flags are gone.
	for _, flag := range []string{"--uploader", "--token"} {
		if out, err := runCommand(t, append(base, flag, "wren@example.com", "--system", "system01", "--code", "game01")...); err == nil {
			t.Errorf("game add %s succeeded\n%s", flag, out)
		}
	}
}

// Flags that name what a command acts on are honored only on the command
// line.
func TestGameFlagsIgnoreEnvironmentVariables(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	t.Setenv("FUINGO_MANAGER", "acct01@example.com")
	t.Setenv("FUINGO_PLAYER", "catbird@example.com")
	t.Setenv("FUINGO_SYSTEM", "system01")
	t.Setenv("FUINGO_CODE", "game01")
	base := []string{"--env", "test", "--db", path, "game"}
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"add"}, ErrAddWhat},
		{[]string{"add", "--player", "wren@example.com"}, ErrMissingSystem},
		{[]string{"add", "--player", "wren@example.com", "--system", "system01"}, ErrMissingCode},
		{[]string{"disable"}, ErrMissingSystem},
		{[]string{"disable", "--system", "system01"}, ErrMissingCode},
		{[]string{"create", "game03"}, ErrMissingSystem},
	} {
		if out, err := runCommand(t, append(base, tc.args...)...); !errors.Is(err, tc.want) {
			t.Errorf("game %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	// With the game named on the command line, the environment's player and
	// manager are ignored: the game itself is disabled.
	mustRun(t, "disabled game system01/game02\n", append(base, "disable", "--system", "system01", "--code", "game02")...)
	if got := players(t, path, game01); len(got) != 2 {
		t.Errorf("players of game01 = %v, want both", got)
	}
	if got := managers(t, path, game01); len(got) != 1 {
		t.Errorf("managers of game01 = %v, want acct01", got)
	}
}

func TestGameDisableEnableGame(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	base := []string{"--env", "test", "--db", path, "game"}
	for range 2 { // disabling twice succeeds
		mustRun(t, "disabled game system01/game01\n", append(base, "disable", "--system", "SYSTEM01", "--code", "GAME01")...)
	}
	if got := players(t, path, game01); len(got) != 0 {
		t.Errorf("an inactive game has players: %v", got)
	}
	// Its managers still manage it.
	if got := managers(t, path, game01); len(got) != 1 {
		t.Errorf("managers of an inactive game = %v", got)
	}
	mustRun(t, "enabled game system01/game01\n", append(base, "enable", "--system", "system01", "--code", "game01")...)
	if got := players(t, path, game01); len(got) != 2 {
		t.Errorf("a re-enabled game has players %v", got)
	}
}

func TestGameDisableEnablePlayer(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	base := []string{"--env", "test", "--db", path, "game"}
	target := []string{"--player", "CATBIRD@example.com", "--system", "system01", "--code", "game01"}
	for range 2 {
		mustRun(t, "disabled player catbird@example.com in game system01/game01\n", append(append(base, "disable"), target...)...)
	}
	if got := players(t, path, game01); !slices.Equal(got, []string{"spoonbill@example.com"}) {
		t.Errorf("players after disabling catbird = %v", got)
	}
	if a, _ := account(t, path, "catbird@example.com"); !a.Active {
		t.Error("disabling a player disabled its account")
	}
	mustRun(t, "enabled player catbird@example.com in game system01/game01\n", append(append(base, "enable"), target...)...)
	if got := players(t, path, game01); len(got) != 2 {
		t.Errorf("players after enabling catbird = %v", got)
	}
}

func TestGameDisableEnableManager(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	base := []string{"--env", "test", "--db", path, "game"}
	target := []string{"--manager", "ACCT01@example.com", "--system", "system01", "--code", "game01"}
	for range 2 {
		mustRun(t, "disabled manager acct01@example.com of game system01/game01\n", append(append(base, "disable"), target...)...)
	}
	if got := managers(t, path, game01); len(got) != 0 {
		t.Errorf("managers of game01 = %v, want none", got)
	}
	if got := managers(t, path, game02); len(got) != 1 {
		t.Errorf("managers of game02 = %v, want acct01", got)
	}
	// A disabled manager cannot be added again, only enabled.
	if out, err := runCommand(t, append(append(base, "add"), target...)...); !errors.Is(err, store.ErrManagerExists) {
		t.Errorf("game add of a disabled manager: %v\n%s", err, out)
	}
	mustRun(t, "enabled manager acct01@example.com of game system01/game01\n", append(append(base, "enable"), target...)...)
	if got := managers(t, path, game01); len(got) != 1 {
		t.Errorf("managers of game01 = %v, want acct01", got)
	}
}

func TestGameDisableEnableRefusals(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	for _, verb := range []string{"disable", "enable"} {
		for _, tc := range []struct {
			args []string
			want error
		}{
			{[]string{"--system", "system01", "--code", "game99"}, store.ErrUnknownGame},
			{[]string{"--system", "system02", "--code", "game01"}, store.ErrUnknownGame},
			{[]string{"--system", "system01"}, ErrMissingCode},
			{[]string{"--code", "game01"}, ErrMissingSystem},
			{[]string{"--system", "system01", "--code", "game 01"}, names.ErrCode},
			{[]string{"--system", "system01", "--code", "game01", "extra"}, ErrUsage},
			{[]string{"--player", "catbird@example.com", "--manager", "acct01@example.com", "--system", "system01", "--code", "game01"}, ErrToggleWhat},
			{[]string{"--player", "catbird@example.com", "--system", "system01", "--code", "game02"}, store.ErrUnknownPlayer},
			{[]string{"--player", "missing@example.com", "--system", "system01", "--code", "game01"}, store.ErrUnknownPlayer},
			{[]string{"--player", "cat bird@example.com", "--system", "system01", "--code", "game01"}, names.ErrEmail},
			{[]string{"--manager", "catbird@example.com", "--system", "system01", "--code", "game01"}, store.ErrUnknownManager},
			{[]string{"--manager", "acct01@example.com", "--system", "system01", "--code", "game99"}, store.ErrUnknownGame},
		} {
			args := append([]string{"--env", "test", "--db", path, "game", verb}, tc.args...)
			if out, err := runCommand(t, args...); !errors.Is(err, tc.want) {
				t.Errorf("%q: err = %v, want %v\n%s", args, err, tc.want, out)
			}
		}
	}
	if got := players(t, path, game01); len(got) != 2 {
		t.Errorf("a refused command changed game01's players to %v", got)
	}
	for _, game := range []store.GameRef{game01, game02} {
		if got := managers(t, path, game); len(got) != 1 {
			t.Errorf("a refused command changed the managers of %s to %v", game, got)
		}
	}
}

// account returns the account with the given email.
func account(t *testing.T, path, email string) (store.Account, bool) {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	a, ok, err := s.FindAccount(t.Context(), email)
	if err != nil {
		t.Fatal(err)
	}
	return a, ok
}

func TestAccountCreateReadsPassword(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	for _, tc := range []struct{ email, input, password string }{
		{"Acct01@Example.com", "a pass phrase\n", "a pass phrase"},
		{"acct02@example.com", "crlf-password\r\nsecond line\n", "crlf-password"},
		{"acct03@example.com", "no-newline", "no-newline"},
	} {
		out, err := runWithInput(t, tc.input, "--env", "test", "--db", path, "account", "create", tc.email)
		if err != nil {
			t.Fatalf("account create %s: %v\n%s", tc.email, err, out)
		}
		email := strings.ToLower(tc.email)
		if !strings.HasPrefix(out, "created account "+email+" (id ") || strings.Contains(out, "password") {
			t.Errorf("account create %s printed %q", tc.email, out)
		}
		a, ok := account(t, path, email)
		if !ok {
			t.Fatalf("account %s not found", email)
		}
		if err := bcrypt.CompareHashAndPassword(a.HashedSecret, []byte(tc.password)); err != nil {
			t.Errorf("%s: password %q does not match: %v", email, tc.password, err)
		}
		if cost, err := bcrypt.Cost(a.HashedSecret); err != nil || cost != bcrypt.MinCost {
			t.Errorf("%s: bcrypt cost %d, %v; want MinCost", email, cost, err)
		}
	}
}

func TestAccountCreateGeneratesPassword(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	out, err := runWithInput(t, "ignored\n", "--env", "test", "--db", path, "account", "create", "--generate-password", "acct01@example.com")
	if err != nil {
		t.Fatalf("account create: %v\n%s", err, out)
	}
	first, password, ok := strings.Cut(out, "password: ")
	if !ok || !strings.HasPrefix(first, "created account acct01@example.com (id ") {
		t.Fatalf("account create printed %q, want the account and a password", out)
	}
	password = strings.TrimSuffix(password, "\n")
	a, _ := account(t, path, "acct01@example.com")
	if err := bcrypt.CompareHashAndPassword(a.HashedSecret, []byte(password)); err != nil {
		t.Errorf("the generated password does not match: %v", err)
	}
	if bcrypt.CompareHashAndPassword(a.HashedSecret, []byte("ignored")) == nil {
		t.Error("account create used standard input with --generate-password")
	}
}

func TestAccountCreateRefusals(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	if out, err := runWithInput(t, "first\n", "--env", "test", "--db", path, "account", "create", "acct01@example.com"); err != nil {
		t.Fatalf("account create: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		input string
		args  []string
		want  error
	}{
		{"second\n", []string{"ACCT01@example.com"}, store.ErrAccountExists},
		{"", []string{"acct02@example.com"}, ErrMissingPassword},
		{"\n", []string{"acct02@example.com"}, ErrMissingPassword},
		{strings.Repeat("x", 73) + "\n", []string{"acct02@example.com"}, bcrypt.ErrPasswordTooLong},
		{"pw\n", []string{"acct 02@example.com"}, names.ErrEmail},
		{"pw\n", []string{}, ErrUsage},
		{"pw\n", []string{"a@example.com", "b@example.com"}, ErrUsage},
	} {
		out, err := runWithInput(t, tc.input, append([]string{"--env", "test", "--db", path, "account", "create"}, tc.args...)...)
		if !errors.Is(err, tc.want) {
			t.Errorf("account create %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	a, _ := account(t, path, "acct01@example.com")
	if err := bcrypt.CompareHashAndPassword(a.HashedSecret, []byte("first")); err != nil {
		t.Errorf("a refused account create changed the password: %v", err)
	}
	if _, ok := account(t, path, "acct02@example.com"); ok {
		t.Error("a refused account create made acct02@example.com")
	}
}

func TestAccountCreateIgnoresGeneratePasswordEnvironmentVariable(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_GENERATE_PASSWORD", "true")
	path := createDatabase(t)
	out, err := runCommand(t, "--env", "test", "--db", path, "account", "create", "acct01@example.com")
	if !errors.Is(err, ErrMissingPassword) {
		t.Errorf("err = %v, want ErrMissingPassword\n%s", err, out)
	}
}

// TestAccountSetPassword sets a password on an account made for a player,
// then replaces it with a generated one.
func TestAccountSetPassword(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	out, err := runWithInput(t, "a pass phrase\r\n", "--env", "test", "--db", path, "account", "set-password", "CATBIRD@example.com")
	if err != nil {
		t.Fatalf("account set-password: %v\n%s", err, out)
	}
	if out != "set the password of account catbird@example.com\n" {
		t.Errorf("account set-password printed %q", out)
	}
	a, _ := account(t, path, "catbird@example.com")
	if err := bcrypt.CompareHashAndPassword(a.HashedSecret, []byte("a pass phrase")); err != nil {
		t.Errorf("the password does not match: %v", err)
	}
	if cost, err := bcrypt.Cost(a.HashedSecret); err != nil || cost != bcrypt.MinCost {
		t.Errorf("bcrypt cost %d, %v; want MinCost", cost, err)
	}
	if got := players(t, path, game01); !slices.Equal(got, []string{"catbird@example.com", "spoonbill@example.com"}) {
		t.Errorf("setting the password changed game01's players to %v", got)
	}

	out, err = runWithInput(t, "ignored\n", "--env", "test", "--db", path, "account", "set-password", "--generate-password", "catbird@example.com")
	if err != nil {
		t.Fatalf("account set-password --generate-password: %v\n%s", err, out)
	}
	first, password, ok := strings.Cut(out, "password: ")
	if !ok || first != "set the password of account catbird@example.com\n" {
		t.Fatalf("account set-password printed %q, want the account and a password", out)
	}
	a, _ = account(t, path, "catbird@example.com")
	if err := bcrypt.CompareHashAndPassword(a.HashedSecret, []byte(strings.TrimSuffix(password, "\n"))); err != nil {
		t.Errorf("the generated password does not match: %v", err)
	}
	for _, old := range []string{"a pass phrase", "ignored"} {
		if bcrypt.CompareHashAndPassword(a.HashedSecret, []byte(old)) == nil {
			t.Errorf("the generated password did not replace %q", old)
		}
	}
}

func TestAccountSetPasswordRefusals(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_GENERATE_PASSWORD", "true")
	path := toggleFixture(t)
	for _, tc := range []struct {
		input string
		args  []string
		want  error
	}{
		{"pw\n", []string{"missing@example.com"}, store.ErrUnknownAccount},
		{"", []string{"catbird@example.com"}, ErrMissingPassword},
		{"\n", []string{"catbird@example.com"}, ErrMissingPassword},
		{strings.Repeat("x", 73) + "\n", []string{"catbird@example.com"}, bcrypt.ErrPasswordTooLong},
		{"pw\n", []string{"cat bird@example.com"}, names.ErrEmail},
		{"pw\n", []string{}, ErrUsage},
		{"pw\n", []string{"a@example.com", "b@example.com"}, ErrUsage},
	} {
		out, err := runWithInput(t, tc.input, append([]string{"--env", "test", "--db", path, "account", "set-password"}, tc.args...)...)
		if !errors.Is(err, tc.want) {
			t.Errorf("account set-password %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	if a, _ := account(t, path, "catbird@example.com"); a.HashedSecret != nil {
		t.Error("a refused account set-password set catbird's password")
	}
	if _, ok := account(t, path, "missing@example.com"); ok {
		t.Error("account set-password made missing@example.com")
	}
}

func TestSchemaVersionIgnoresEnvironmentVariable(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_SCHEMA", "true")
	out, err := runCommand(t, "--env", "test", "--db", createDatabase(t), "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	if want := fuingo.Version().String() + "\n"; out != want {
		t.Errorf("version printed %q, want %q", out, want)
	}
}

func TestAccountDisableEnable(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	base := []string{"--env", "test", "--db", path, "account"}
	for range 2 { // disabling twice succeeds
		mustRun(t, "disabled account acct01@example.com\n", append(base, "disable", "ACCT01@example.com")...)
	}
	if a, _ := account(t, path, "acct01@example.com"); a.Active {
		t.Error("a disabled account is active")
	}
	for _, game := range []store.GameRef{game01, game02} {
		if got := managers(t, path, game); len(got) != 0 {
			t.Errorf("a disabled account still manages %s: %v", game, got)
		}
	}
	mustRun(t, "enabled account acct01@example.com\n", append(base, "enable", "acct01@example.com")...)
	if a, _ := account(t, path, "acct01@example.com"); !a.Active {
		t.Error("a re-enabled account is inactive")
	}
	if got := managers(t, path, game01); len(got) != 1 {
		t.Errorf("a re-enabled account does not manage game01: %v", got)
	}
}

// TestAccountDisableStopsPlaying checks that a player's account, created by
// game add --player without a password, can be disabled and enabled.
func TestAccountDisableStopsPlaying(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	if a, ok := account(t, path, "catbird@example.com"); !ok || a.HashedSecret != nil {
		t.Fatalf("account = %+v, %v; want one without a secret", a, ok)
	}
	for _, tc := range []struct {
		verb string
		want []string
	}{
		{"disable", []string{"spoonbill@example.com"}},
		{"enable", []string{"catbird@example.com", "spoonbill@example.com"}},
	} {
		args := []string{"--env", "test", "--db", path, "account", tc.verb, "catbird@example.com"}
		if out, err := runCommand(t, args...); err != nil {
			t.Fatalf("%q: %v\n%s", args, err, out)
		}
		if got := players(t, path, game01); !slices.Equal(got, tc.want) {
			t.Errorf("after account %s, game01's players = %v, want %v", tc.verb, got, tc.want)
		}
	}
}

func TestAccountDisableEnableRefusals(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t)
	for _, verb := range []string{"disable", "enable"} {
		for _, tc := range []struct {
			args []string
			want error
		}{
			{[]string{"missing@example.com"}, store.ErrUnknownAccount},
			{[]string{"acct 01@example.com"}, names.ErrEmail},
			{[]string{}, ErrUsage},
			{[]string{"acct01@example.com", "extra"}, ErrUsage},
		} {
			args := append([]string{"--env", "test", "--db", path, "account", verb}, tc.args...)
			if out, err := runCommand(t, args...); !errors.Is(err, tc.want) {
				t.Errorf("%q: err = %v, want %v\n%s", args, err, tc.want, out)
			}
		}
	}
	if a, _ := account(t, path, "acct01@example.com"); !a.Active {
		t.Error("a refused command disabled acct01@example.com")
	}
}
