// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// clearEnv keeps the developer's environment out of the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FUINGO_ENV", "FUINGO_DB", "FUINGO_PEPPER", "FUINGO_SCHEMA", "FUINGO_TOKEN", "FUINGO_GENERATE_PASSWORD", "FUINGO_MANAGER", "FUINGO_UPLOADER", "FUINGO_CODE"} {
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

// games returns the codes of the games in the database at path.
func games(t *testing.T, path string) []string {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	codes, err := s.Games(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return codes
}

func TestGameCreate(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	out, err := runCommand(t, "--env", "test", "--db", path, "game", "create", "Game-01")
	if err != nil {
		t.Fatalf("game create: %v\n%s", err, out)
	}
	if want := "created game game-01 (id 1)\n"; out != want {
		t.Errorf("game create printed %q, want %q", out, want)
	}
	if got := games(t, path); len(got) != 1 || got[0] != "game-01" {
		t.Errorf("games = %q, want [game-01]", got)
	}
}

func TestGameCreateRefusesExistingCode(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	if out, err := runCommand(t, "--env", "test", "--db", path, "game", "create", "game01"); err != nil {
		t.Fatalf("game create: %v\n%s", err, out)
	}
	for _, code := range []string{"game01", "GAME01"} {
		out, err := runCommand(t, "--env", "test", "--db", path, "game", "create", code)
		if !errors.Is(err, store.ErrGameExists) {
			t.Errorf("game create %s: err = %v, want ErrGameExists\n%s", code, err, out)
		}
	}
	if got := games(t, path); len(got) != 1 {
		t.Errorf("games = %q, want only game01", got)
	}
}

func TestGameCreateRefusesInvalidCodes(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	for _, code := range []string{"", " ", "game 01", "game\t01", "game\x7f", "game\x1f", "gämé"} {
		out, err := runCommand(t, "--env", "test", "--db", path, "game", "create", code)
		if !errors.Is(err, ErrGameCode) {
			t.Errorf("game create %q: err = %v, want ErrGameCode\n%s", code, err, out)
		}
	}
	for _, args := range [][]string{{}, {"game01", "game02"}} {
		out, err := runCommand(t, append([]string{"--env", "test", "--db", path, "game", "create"}, args...)...)
		if !errors.Is(err, ErrUsage) {
			t.Errorf("game create %q: err = %v, want ErrUsage\n%s", args, err, out)
		}
	}
	if got := games(t, path); len(got) != 0 {
		t.Errorf("games = %q, want none", got)
	}
}

func TestGameCreateRefusesOtherSchemaVersions(t *testing.T) {
	clearEnv(t)
	path := createDatabase(t)
	setSchemaVersion(t, path, store.CurrentSchemaVersion()-1)
	out, err := runCommand(t, "--env", "test", "--db", path, "game", "create", "game01")
	if !errors.Is(err, store.ErrOutdatedSchema) {
		t.Errorf("err = %v, want ErrOutdatedSchema\n%s", err, out)
	}
}

const pepper = "test-pepper"

// createDatabaseWithGames makes a current database holding the given games.
func createDatabaseWithGames(t *testing.T, codes ...string) string {
	t.Helper()
	path := createDatabase(t)
	for _, code := range codes {
		if out, err := runCommand(t, "--env", "test", "--db", path, "game", "create", code); err != nil {
			t.Fatalf("game create %s: %v\n%s", code, err, out)
		}
	}
	return path
}

// uploaderExists reports whether the database accepts the credentials.
func uploaderExists(t *testing.T, path, game, email, token string) bool {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	gameID, ok, err := s.GameID(t.Context(), game)
	if err != nil || !ok {
		t.Fatalf("GameID(%s) = %v, %v", game, ok, err)
	}
	_, ok, err = s.FindUploader(t.Context(), credential.Hash(pepper, email, gameID, token))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// account returns the API account with the given email.
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

func TestUploaderCreateGeneratesToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	path := createDatabaseWithGames(t, "game01")
	out, err := runCommand(t, "--env", "test", "--db", path, "uploader", "create", "GAME01", "CatBird@Example.com")
	if err != nil {
		t.Fatalf("uploader create: %v\n%s", err, out)
	}
	first, token, ok := strings.Cut(out, "token: ")
	if want := "created uploader catbird@example.com in game game01\n"; !ok || first != want {
		t.Fatalf("uploader create printed %q, want %q and a token", out, want)
	}
	token = strings.TrimSuffix(token, "\n")
	if _, err := uuid.Parse(token); err != nil {
		t.Errorf("generated token %q is not a UUID: %v", token, err)
	}
	if !uploaderExists(t, path, "game01", "catbird@example.com", token) {
		t.Error("the generated token does not authorize the uploader")
	}
}

func TestUploaderCreateWithToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	path := createDatabaseWithGames(t, "game01", "game02")
	const token = "my-chosen-token"
	// The same email and token may be used in two games.
	for _, game := range []string{"game01", "game02"} {
		out, err := runCommand(t, "--env", "test", "--db", path, "uploader", "create", "--token", token, game, "catbird@example.com")
		if err != nil {
			t.Fatalf("uploader create %s: %v\n%s", game, err, out)
		}
		if strings.Contains(out, token) {
			t.Errorf("uploader create echoed the token: %q", out)
		}
		if !uploaderExists(t, path, game, "catbird@example.com", token) {
			t.Errorf("%s: the token does not authorize the uploader", game)
		}
	}
}

func TestUploaderCreateRefusals(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	path := createDatabaseWithGames(t, "game01")
	const token = "secret-token"
	if out, err := runCommand(t, "--env", "test", "--db", path, "uploader", "create", "--token", token, "game01", "catbird@example.com"); err != nil {
		t.Fatalf("uploader create: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"--token", "other-token", "game01", "CATBIRD@example.com"}, store.ErrUploaderExists},
		{[]string{"--token", token, "game99", "spoonbill@example.com"}, store.ErrUnknownGame},
		{[]string{"--token", "secret token", "game01", "spoonbill@example.com"}, ErrToken},
		{[]string{"--token", "secret\x7ftoken", "game01", "spoonbill@example.com"}, ErrToken},
		{[]string{"game01", "spoon bill@example.com"}, ErrEmail},
		{[]string{"game01", "spoonbill@exämple.com"}, ErrEmail},
		{[]string{"game 01", "spoonbill@example.com"}, ErrGameCode},
		{[]string{"game01"}, ErrUsage},
		{[]string{"game01", "spoonbill@example.com", "extra"}, ErrUsage},
	} {
		out, err := runCommand(t, append([]string{"--env", "test", "--db", path, "uploader", "create"}, tc.args...)...)
		if !errors.Is(err, tc.want) {
			t.Errorf("uploader create %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
		if strings.Contains(out, "secret") || (err != nil && strings.Contains(err.Error(), "secret")) {
			t.Errorf("uploader create %q revealed the token: %v\n%s", tc.args, err, out)
		}
	}
}

func TestUploaderCreateRequiresPepper(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	path := createDatabaseWithGames(t, "game01")
	os.Unsetenv("FUINGO_PEPPER")
	out, err := runCommand(t, "--env", "test", "--db", path, "uploader", "create", "game01", "catbird@example.com")
	if !errors.Is(err, cli.ErrMissingPepper) {
		t.Errorf("err = %v, want ErrMissingPepper\n%s", err, out)
	}
}

func TestUploaderCreateIgnoresTokenEnvironmentVariable(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	t.Setenv("FUINGO_TOKEN", "from-the-environment")
	path := createDatabaseWithGames(t, "game01")
	out, err := runCommand(t, "--env", "test", "--db", path, "uploader", "create", "game01", "catbird@example.com")
	if err != nil {
		t.Fatalf("uploader create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "token: ") {
		t.Errorf("uploader create did not generate a token: %q", out)
	}
	if uploaderExists(t, path, "game01", "catbird@example.com", "from-the-environment") {
		t.Error("uploader create used FUINGO_TOKEN")
	}
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
		if err := bcrypt.CompareHashAndPassword(a.PasswordHash, []byte(tc.password)); err != nil {
			t.Errorf("%s: password %q does not match: %v", email, tc.password, err)
		}
		if cost, err := bcrypt.Cost(a.PasswordHash); err != nil || cost != bcrypt.MinCost {
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
	if err := bcrypt.CompareHashAndPassword(a.PasswordHash, []byte(password)); err != nil {
		t.Errorf("the generated password does not match: %v", err)
	}
	if bcrypt.CompareHashAndPassword(a.PasswordHash, []byte("ignored")) == nil {
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
		{"pw\n", []string{"acct 02@example.com"}, ErrEmail},
		{"pw\n", []string{}, ErrUsage},
		{"pw\n", []string{"a@example.com", "b@example.com"}, ErrUsage},
	} {
		out, err := runWithInput(t, tc.input, append([]string{"--env", "test", "--db", path, "account", "create"}, tc.args...)...)
		if !errors.Is(err, tc.want) {
			t.Errorf("account create %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	a, _ := account(t, path, "acct01@example.com")
	if err := bcrypt.CompareHashAndPassword(a.PasswordHash, []byte("first")); err != nil {
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

// managers returns the emails of the game's managers.
func managers(t *testing.T, path, game string) []string {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	emails, err := s.Managers(t.Context(), game)
	if err != nil {
		t.Fatal(err)
	}
	return emails
}

// createDatabaseWithAccounts makes a current database holding game01, game02,
// and the given API accounts.
func createDatabaseWithAccounts(t *testing.T, emails ...string) string {
	t.Helper()
	path := createDatabaseWithGames(t, "game01", "game02")
	for _, email := range emails {
		if out, err := runCommand(t, "--env", "test", "--db", path, "account", "create", "--generate-password", email); err != nil {
			t.Fatalf("account create %s: %v\n%s", email, err, out)
		}
	}
	return path
}

func TestGameAddManager(t *testing.T) {
	clearEnv(t)
	path := createDatabaseWithAccounts(t, "acct01@example.com", "acct02@example.com")
	for _, args := range [][]string{
		{"game", "add", "--manager", "Acct01@Example.com", "--code", "GAME01"},
		{"game", "add", "--code", "game01", "--manager", "acct02@example.com"},
		{"game", "add", "--manager", "acct01@example.com", "--code", "game02"},
	} {
		out, err := runCommand(t, append([]string{"--env", "test", "--db", path}, args...)...)
		if err != nil {
			t.Fatalf("%q: %v\n%s", args, err, out)
		}
		if !strings.HasPrefix(out, "added manager acct0") {
			t.Errorf("%q printed %q", args, out)
		}
	}
	for game, want := range map[string]string{
		"game01": "acct01@example.com,acct02@example.com",
		"game02": "acct01@example.com",
	} {
		if got := strings.Join(managers(t, path, game), ","); got != want {
			t.Errorf("managers of %s = %s, want %s", game, got, want)
		}
	}
}

func TestGameAddManagerRefusals(t *testing.T) {
	clearEnv(t)
	path := createDatabaseWithAccounts(t, "acct01@example.com")
	if out, err := runCommand(t, "--env", "test", "--db", path, "game", "add", "--manager", "acct01@example.com", "--code", "game01"); err != nil {
		t.Fatalf("game add: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"--manager", "ACCT01@example.com", "--code", "game01"}, store.ErrManagerExists},
		{[]string{"--manager", "acct01@example.com", "--code", "game99"}, store.ErrUnknownGame},
		{[]string{"--manager", "missing@example.com", "--code", "game02"}, store.ErrUnknownAccount},
		{[]string{"--manager", "catbird@example.com", "--code", "game02"}, store.ErrUnknownAccount},
		{[]string{"--manager", "acct 01@example.com", "--code", "game02"}, ErrEmail},
		{[]string{"--manager", "acct01@example.com", "--code", "game 02"}, ErrGameCode},
		{[]string{"--code", "game02"}, ErrAddWhat},
		{[]string{"--manager", "acct01@example.com", "--uploader", "catbird@example.com", "--code", "game02"}, ErrAddWhat},
		{[]string{"--manager", "acct01@example.com", "--code", "game02", "--token", "t"}, ErrTokenNeedsUploader},
		{[]string{"--manager", "acct01@example.com"}, ErrMissingCode},
		{[]string{"--manager", "acct01@example.com", "--code", "game02", "extra"}, ErrUsage},
	} {
		out, err := runCommand(t, append([]string{"--env", "test", "--db", path, "game", "add"}, tc.args...)...)
		if !errors.Is(err, tc.want) {
			t.Errorf("game add %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
	}
	if got := managers(t, path, "game02"); len(got) != 0 {
		t.Errorf("managers of game02 = %v, want none", got)
	}
}

func TestGameAddIgnoresEnvironmentVariables(t *testing.T) {
	clearEnv(t)
	path := createDatabaseWithAccounts(t, "acct01@example.com")
	t.Setenv("FUINGO_MANAGER", "acct01@example.com")
	t.Setenv("FUINGO_CODE", "game01")
	out, err := runCommand(t, "--env", "test", "--db", path, "game", "add")
	if !errors.Is(err, ErrAddWhat) {
		t.Errorf("err = %v, want ErrAddWhat\n%s", err, out)
	}
	if got := managers(t, path, "game01"); len(got) != 0 {
		t.Errorf("managers of game01 = %v, want none", got)
	}
}

func TestGameAddUploader(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	path := createDatabaseWithGames(t, "game01", "game02")

	out, err := runCommand(t, "--env", "test", "--db", path, "game", "add", "--uploader", "CatBird@Example.com", "--code", "GAME01")
	if err != nil {
		t.Fatalf("game add --uploader: %v\n%s", err, out)
	}
	first, token, ok := strings.Cut(out, "token: ")
	if want := "added uploader catbird@example.com to game game01\n"; !ok || first != want {
		t.Fatalf("game add --uploader printed %q, want %q and a token", out, want)
	}
	token = strings.TrimSuffix(token, "\n")
	if _, err := uuid.Parse(token); err != nil {
		t.Errorf("generated token %q is not a UUID: %v", token, err)
	}
	if !uploaderExists(t, path, "game01", "catbird@example.com", token) {
		t.Error("the generated token does not authorize the uploader")
	}

	const chosen = "my-chosen-token"
	out, err = runCommand(t, "--env", "test", "--db", path, "game", "add", "--token", chosen, "--code", "game02", "--uploader", "catbird@example.com")
	if err != nil {
		t.Fatalf("game add --uploader --token: %v\n%s", err, out)
	}
	if want := "added uploader catbird@example.com to game game02\n"; out != want {
		t.Errorf("game add --uploader --token printed %q, want %q", out, want)
	}
	if !uploaderExists(t, path, "game02", "catbird@example.com", chosen) {
		t.Error("the given token does not authorize the uploader")
	}
}

func TestGameAddUploaderRefusals(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	path := createDatabaseWithGames(t, "game01")
	if out, err := runCommand(t, "--env", "test", "--db", path, "game", "add", "--uploader", "catbird@example.com", "--code", "game01"); err != nil {
		t.Fatalf("game add --uploader: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		args []string
		want error
	}{
		{[]string{"--uploader", "CATBIRD@example.com", "--code", "game01"}, store.ErrUploaderExists},
		{[]string{"--uploader", "tern@example.com", "--code", "game99"}, store.ErrUnknownGame},
		{[]string{"--uploader", "tern@example.com", "--code", "game01", "--token", "secret token"}, ErrToken},
		{[]string{"--uploader", "tern @example.com", "--code", "game01"}, ErrEmail},
		{[]string{"--uploader", "tern@example.com"}, ErrMissingCode},
	} {
		out, err := runCommand(t, append([]string{"--env", "test", "--db", path, "game", "add"}, tc.args...)...)
		if !errors.Is(err, tc.want) {
			t.Errorf("game add %q: err = %v, want %v\n%s", tc.args, err, tc.want, out)
		}
		if strings.Contains(out, "secret") || (err != nil && strings.Contains(err.Error(), "secret")) {
			t.Errorf("game add %q revealed the token: %v\n%s", tc.args, err, out)
		}
	}
	os.Unsetenv("FUINGO_PEPPER")
	out, err := runCommand(t, "--env", "test", "--db", path, "game", "add", "--uploader", "tern@example.com", "--code", "game01")
	if !errors.Is(err, cli.ErrMissingPepper) {
		t.Errorf("without a pepper: err = %v, want ErrMissingPepper\n%s", err, out)
	}
}

func TestGameAddUploaderIgnoresEnvironmentVariables(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	path := createDatabaseWithGames(t, "game01")
	t.Setenv("FUINGO_UPLOADER", "catbird@example.com")
	t.Setenv("FUINGO_TOKEN", "from-the-environment")
	out, err := runCommand(t, "--env", "test", "--db", path, "game", "add", "--code", "game01")
	if !errors.Is(err, ErrAddWhat) {
		t.Errorf("err = %v, want ErrAddWhat\n%s", err, out)
	}
	out, err = runCommand(t, "--env", "test", "--db", path, "game", "add", "--uploader", "tern@example.com", "--code", "game01")
	if err != nil {
		t.Fatalf("game add --uploader: %v\n%s", err, out)
	}
	if !strings.Contains(out, "token: ") || uploaderExists(t, path, "game01", "tern@example.com", "from-the-environment") {
		t.Errorf("game add --uploader used FUINGO_TOKEN: %q", out)
	}
}
