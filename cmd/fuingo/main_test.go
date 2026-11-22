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
	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/mail"
	"github.com/mdhender/fuingo/internal/mail/mailtest"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// publicURL is the public origin the tests give the commands.
const publicURL = "https://fuingo.example.com"

// sent records the mail the commands send in place of Mailgun.
var sent = &mailtest.Recorder{}

// clearEnv keeps the developer's environment, and real mail, out of the
// test: mail goes to sent, which starts empty.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"FUINGO_ENV", "FUINGO_DB", "FUINGO_SCHEMA", "FUINGO_GENERATE_PASSWORD", "FUINGO_MANAGER", "FUINGO_PLAYER", "FUINGO_SYSTEM", "FUINGO_CODE",
		"FUINGO_PUBLIC_URL", "FUINGO_MAILGUN_DOMAIN", "FUINGO_MAILGUN_API_KEY", "FUINGO_MAILGUN_FROM", "FUINGO_MAILGUN_API_BASE", "FUINGO_MAILGUN_SANDBOX_TO_ADDR",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	sent.Reset()
	newSender = func(string) (mail.Sender, error) { return sent, nil }
	t.Cleanup(func() { newSender = func(string) (mail.Sender, error) { return sent, nil } })
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
	mustRun(t, "added player wren@example.com to game olympia/g3 (id 1)\nmailed wren@example.com an invitation\n", "--env", "test", "--db", path, "--public-url", publicURL, "game", "add", "--player", "wren@example.com", "--system", "olympia", "--code", "g3")
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
	base := []string{"--env", "test", "--db", path, "--public-url", publicURL, "game", "add"}
	mustRun(t, "added player catbird@example.com to game system01/game01 (id 1)\nmailed catbird@example.com an invitation\n", append(base, "--player", "Catbird@Example.com", "--system", "System01", "--code", "Game01")...)
	// The account is new, has no password, and was sent its invitation.
	if a, ok := account(t, path, "catbird@example.com"); !ok || a.HashedSecret != nil || !a.Active {
		t.Errorf("account = %+v, %v; want an active one without a password", a, ok)
	}
	if got := mailed(t); !slices.Equal(got, []string{"catbird@example.com invitation"}) {
		t.Errorf("mail = %v, want one invitation", got)
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
	// Only the new account was mailed: joining a game sends nothing.
	if got := mailed(t); len(got) != 1 {
		t.Errorf("mail = %v, want only catbird's invitation", got)
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

// mailed returns the recipients of the mail sent since clearEnv, each with
// the kind of message: "invitation" or "reset".
func mailed(t *testing.T) []string {
	t.Helper()
	var got []string
	for _, m := range sent.Messages() {
		kind := "reset"
		if m.Subject == "Your Fuingo account" {
			kind = "invitation"
		}
		link, token := mailtest.Link(m)
		if !strings.HasPrefix(link, publicURL+"/account/password?token=") || token == "" {
			t.Errorf("mail to %s has link %q, want one under %s", m.To, link, publicURL)
		}
		got = append(got, m.To+" "+kind)
	}
	return got
}

// usePassword redeems the link in the mail and reports whether it worked.
func usePassword(t *testing.T, path string, m mail.Message, password string) bool {
	t.Helper()
	s := open(t, path)
	_, token := mailtest.Link(m)
	_, ok, err := (&links.Service{Store: s}).SetPassword(t.Context(), token, password)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestAccountCreateSendsInvitation(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	base := []string{"--env", "test", "--db", path, "--public-url", publicURL, "account", "create"}
	out, err := runCommand(t, append(base, "ACCT01@Example.com")...)
	if err != nil {
		t.Fatalf("account create: %v\n%s", err, out)
	}
	if out != "created account acct01@example.com (id 1)\nmailed acct01@example.com an invitation\n" {
		t.Errorf("account create printed %q", out)
	}
	// The account has no password until its owner chooses one.
	if a, ok := account(t, path, "acct01@example.com"); !ok || a.HashedSecret != nil || !a.Active {
		t.Errorf("account = %+v, %v; want an active one without a password", a, ok)
	}
	if got := mailed(t); !slices.Equal(got, []string{"acct01@example.com invitation"}) {
		t.Fatalf("mail = %v, want one invitation", got)
	}
	// No token or link is printed.
	_, token := mailtest.Link(sent.Messages()[0])
	if strings.Contains(out, token) || strings.Contains(out, "token") {
		t.Errorf("account create printed the link: %q", out)
	}
	// The link sets the password, once.
	if !usePassword(t, path, sent.Messages()[0], "a pass phrase") {
		t.Fatal("the invitation's link did not work")
	}
	a, _ := account(t, path, "acct01@example.com")
	if err := bcrypt.CompareHashAndPassword(a.HashedSecret, []byte("a pass phrase")); err != nil {
		t.Errorf("the password does not match: %v", err)
	}
	if usePassword(t, path, sent.Messages()[0], "another pass phrase") {
		t.Error("the invitation's link worked twice")
	}
	// The public URL may come from the environment.
	t.Setenv("FUINGO_PUBLIC_URL", publicURL+"/")
	mustRun(t, "created account acct02@example.com (id 2)\nmailed acct02@example.com an invitation\n", "--env", "test", "--db", path, "account", "create", "acct02@example.com")
	if got := mailed(t); len(got) != 2 {
		t.Errorf("mail = %v, want two", got)
	}
}

func TestAccountCreateRefusals(t *testing.T) {
	clearEnv(t)
	path := createDatabaseWithGames(t, "acct01@example.com")
	base := []string{"--env", "test", "--db", path, "--public-url", publicURL, "account", "create"}
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"ACCT01@example.com"}, store.ErrAccountExists},
		{[]string{"acct 02@example.com"}, names.ErrEmail},
		{[]string{}, ErrUsage},
		{[]string{"acct02@example.com", "extra"}, ErrUsage},
		// The commands that set or printed passwords are gone.
		{[]string{"--generate-password", "acct02@example.com"}, nil},
	} {
		out, err := runWithInput(t, "a password\n", append(base, tc.args...)...)
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("account create %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	if out, err := runWithInput(t, "a password\n", "--env", "test", "--db", path, "account", "set-password", "acct01@example.com"); err == nil {
		t.Errorf("account set-password succeeded\n%s", out)
	}
	if a, _ := account(t, path, "acct01@example.com"); string(a.HashedSecret) != "hash" {
		t.Errorf("a refused command changed the password hash to %q", a.HashedSecret)
	}
	if _, ok := account(t, path, "acct02@example.com"); ok {
		t.Error("a refused account create made an account")
	}
	if got := mailed(t); len(got) != 0 {
		t.Errorf("refused commands sent mail: %v", got)
	}
}

// A command that would send mail changes nothing when it cannot.
func TestMailCommandsNeedMailSettings(t *testing.T) {
	clearEnv(t)
	path := createDatabaseWithGames(t, "acct01@example.com")
	commands := map[string][]string{
		"account create":         {"account", "create", "new@example.com"},
		"account reset-password": {"account", "reset-password", "acct01@example.com"},
		"game add --player":      {"game", "add", "--player", "new@example.com", "--system", "system01", "--code", "game01"},
	}
	check := func(why string, want error, root ...string) {
		t.Helper()
		for name, args := range commands {
			out, err := runCommand(t, append(append([]string{"--env", "test", "--db", path}, root...), args...)...)
			if !errors.Is(err, want) {
				t.Errorf("%s, %s: err = %v, want %v\n%s", name, why, err, want, out)
			}
		}
		if _, ok := account(t, path, "new@example.com"); ok {
			t.Fatalf("%s: a refused command made the account", why)
		}
		if got := players(t, path, game01); len(got) != 0 {
			t.Fatalf("%s: a refused command added a player: %v", why, got)
		}
		if got := mailed(t); len(got) != 0 {
			t.Fatalf("%s: mail = %v", why, got)
		}
	}
	check("no public URL", ErrMissingPublicURL)
	check("a public URL that is not an origin", links.ErrPublicURL, "--public-url", "https://fuingo.example.com/app")
	check("plain http", links.ErrPublicURL, "--public-url", "http://fuingo.example.com")
	newSender = mail.SenderFromEnv // as the program runs: no Mailgun settings here
	check("no Mailgun settings", mail.ErrNotConfigured, "--public-url", publicURL)
	t.Setenv("FUINGO_MAILGUN_DOMAIN", "mg.example.com")
	check("incomplete Mailgun settings", mail.ErrIncomplete, "--public-url", publicURL)

	// Adding an existing account as a player sends nothing, so needs nothing.
	os.Unsetenv("FUINGO_MAILGUN_DOMAIN")
	mustRun(t, "added player acct01@example.com to game system01/game01 (id 1)\n", "--env", "test", "--db", path, "game", "add", "--player", "acct01@example.com", "--system", "system01", "--code", "game01")
}

// In development and agent the links point at the shared development proxy.
func TestPublicURLDefaultsInDevelopment(t *testing.T) {
	for _, env := range []string{"development", "agent"} {
		clearEnv(t)
		t.Chdir(t.TempDir()) // no environment files
		path := createDatabase(t)
		if out, err := runCommand(t, "--env", env, "--db", path, "account", "create", "acct01@example.com"); err != nil {
			t.Fatalf("%s: %v\n%s", env, err, out)
		}
		link, _ := mailtest.Link(sent.Messages()[0])
		if !strings.HasPrefix(link, "https://htmx-app.localhost:8443/account/password?token=") {
			t.Errorf("%s: link %q", env, link)
		}
	}
}

func TestAccountCreateWhenMailFails(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	sent.Fail(mail.ErrMailgunRejected)
	out, err := runCommand(t, "--env", "test", "--db", path, "--public-url", publicURL, "account", "create", "acct01@example.com")
	if !errors.Is(err, mail.ErrMailgunRejected) || !strings.Contains(err.Error(), "fuingo account reset-password acct01@example.com") {
		t.Errorf("err = %v, want the mail failure and how to send another\n%s", err, out)
	}
	// The account stays; a reset sends its invitation.
	if _, ok := account(t, path, "acct01@example.com"); !ok {
		t.Fatal("the account was not kept")
	}
	sent.Fail(nil)
	mustRun(t, "mailed acct01@example.com a link to choose a password\n", "--env", "test", "--db", path, "--public-url", publicURL, "account", "reset-password", "ACCT01@example.com")
	if got := mailed(t); !slices.Equal(got, []string{"acct01@example.com invitation"}) {
		t.Errorf("mail = %v, want an invitation, since the account has no password", got)
	}
}

func TestAccountResetPassword(t *testing.T) {
	clearEnv(t)
	path := toggleFixture(t) // acct01 has a password; catbird has none
	base := []string{"--env", "test", "--db", path, "--public-url", publicURL, "account", "reset-password"}
	mustRun(t, "mailed acct01@example.com a link to choose a password\n", append(base, "acct01@example.com")...)
	mustRun(t, "mailed acct01@example.com a link to choose a password\n", append(base, "acct01@example.com")...)
	mustRun(t, "mailed catbird@example.com a link to choose a password\n", append(base, "catbird@example.com")...)
	want := []string{"acct01@example.com reset", "acct01@example.com reset", "catbird@example.com invitation"}
	if got := mailed(t); !slices.Equal(got, want) {
		t.Fatalf("mail = %v, want %v", got, want)
	}
	// Asking does not change the password: only the owner's choice does.
	if a, _ := account(t, path, "acct01@example.com"); string(a.HashedSecret) != "hash" {
		t.Errorf("reset-password changed the password hash to %q", a.HashedSecret)
	}
	// The newer link replaced the older one.
	msgs := sent.Messages()
	if usePassword(t, path, msgs[0], "a pass phrase") {
		t.Error("a link worked after a newer one was sent")
	}
	if !usePassword(t, path, msgs[1], "a pass phrase") {
		t.Error("the newest link did not work")
	}
	if a, _ := account(t, path, "acct01@example.com"); bcrypt.CompareHashAndPassword(a.HashedSecret, []byte("a pass phrase")) != nil {
		t.Error("the link did not set the password")
	}
	// One account's link does nothing to another's.
	if a, _ := account(t, path, "catbird@example.com"); a.HashedSecret != nil {
		t.Error("acct01's link set catbird's password")
	}

	mustRun(t, "disabled account catbird@example.com\n", "--env", "test", "--db", path, "account", "disable", "catbird@example.com")
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"missing@example.com"}, store.ErrUnknownAccount},
		{[]string{"catbird@example.com"}, ErrInactiveAccount},
		{[]string{"acct 01@example.com"}, names.ErrEmail},
		{[]string{}, ErrUsage},
		{[]string{"acct01@example.com", "extra"}, ErrUsage},
	} {
		if out, err := runCommand(t, append(base, tc.args...)...); !errors.Is(err, tc.want) {
			t.Errorf("account reset-password %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	if got := mailed(t); len(got) != 3 {
		t.Errorf("refused commands sent mail: %v", got)
	}
	// A disabled account's earlier link does not work either.
	if usePassword(t, path, msgs[2], "a pass phrase") {
		t.Error("a disabled account's link worked")
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
