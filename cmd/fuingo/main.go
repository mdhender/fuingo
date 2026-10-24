// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command fuingo is the operators' CLI for the application data. Its commands
// follow the pattern fuingo <noun> <verb>.
//
//	fuingo version
//	fuingo [--env ENV] [--db PATH] version --schema
//	fuingo [--env ENV] [--db PATH] game create CODE
//	fuingo [--env ENV] [--db PATH] game add --manager EMAIL --code CODE
//	fuingo [--env ENV] [--db PATH] uploader create [--token TOKEN] GAME EMAIL
//	fuingo [--env ENV] [--db PATH] account create [--generate-password] EMAIL
//
// Every flag can also be set with a FUINGO_ environment variable:
//
//	--env   FUINGO_ENV   development, test, production, or agent (required by every
//	                     command but version without --schema)
//	--db    FUINGO_DB    path to the database file (required by commands that use it;
//	                     :memory: is refused)
//
// uploader create also requires FUINGO_PEPPER.
//
// --schema, --token, --generate-password, --manager, and --code are honored
// only on the command line; their FUINGO_ variables are ignored. version reads no environment
// unless --schema is on the command line.
//
// Commands that use the database only open one that is already at the schema
// version this program expects. fuingo never creates a database file or
// migrates one; fuingodb does.
//
// Game codes and emails are lowercased with strings.ToLower, as uploads are.
// Game codes, emails, and tokens must be valid header words: one or more
// bytes in 0x21-0x7E. New games and uploaders are active.
//
// game add --manager makes an existing API account a manager of an existing
// game.
//
// uploader create generates a random UUID for the token unless --token is
// given, and prints a generated token once; only its hash is stored.
// account create reads the password from the first line of standard input,
// or, with --generate-password, generates one and prints it once. Passwords
// are hashed with bcrypt at bcrypt.MinCost and are at most 72 bytes.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
	"golang.org/x/crypto/bcrypt"
)

const (
	ErrMissingDatabase = cerrs.Error("--db (FUINGO_DB) is required")
	ErrMemoryDatabase  = cerrs.Error("fuingo requires a database file, not :memory:")
	ErrEmail           = cerrs.Error("email must be one or more printable ASCII bytes with no spaces")
	ErrGameCode        = cerrs.Error("game code must be one or more printable ASCII bytes with no spaces")
	ErrMissingCode     = cerrs.Error("--code is required")
	ErrMissingManager  = cerrs.Error("--manager is required")
	ErrMissingPassword = cerrs.Error("no password on standard input; pipe one in or use --generate-password")
	ErrToken           = cerrs.Error("token must be one or more printable ASCII bytes with no spaces")
	ErrUsage           = cerrs.Error("wrong number of arguments")
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, ff.ErrHelp) {
			fmt.Fprintf(os.Stderr, "fuingo: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var env, path string
	rootFlags := ff.NewFlagSet("fuingo")
	rootFlags.StringVar(&env, 0, "env", "", "environment: development, test, production, or agent (FUINGO_ENV)")
	rootFlags.StringVar(&path, 0, "db", "", "path to the database file (FUINGO_DB)")

	var schema bool
	versionFlags := ff.NewFlagSet("version").SetParent(rootFlags)
	versionFlags.BoolVar(&schema, 0, "schema", "print the database's schema version instead (command line only)")
	version := &ff.Command{
		Name:      "version",
		Usage:     "fuingo [--db PATH] version [--schema]",
		ShortHelp: "print the program's version, or the database's schema version",
		Flags:     versionFlags,
		Exec: func(ctx context.Context, args []string) error {
			if !schema {
				fmt.Fprintln(stdout, fuingo.Version().String())
				return nil
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				v, err := s.SchemaVersion(ctx)
				if err != nil {
					return err
				}
				fmt.Fprintln(stdout, v)
				return nil
			})
		},
	}

	gameCreate := &ff.Command{
		Name:      "create",
		Usage:     "fuingo [--db PATH] game create CODE",
		ShortHelp: "create an active game",
		Flags:     ff.NewFlagSet("game create").SetParent(rootFlags),
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("game create CODE: %w", ErrUsage)
			}
			code, err := gameCode(args[0])
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				id, err := s.CreateGame(ctx, code)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "created game %s (id %d)\n", code, id)
				return nil
			})
		},
	}
	var manager, addCode string
	gameAddFlags := ff.NewFlagSet("game add").SetParent(rootFlags)
	gameAddFlags.StringVar(&manager, 0, "manager", "", "email of the API account to make a manager (command line only)")
	gameAddFlags.StringVar(&addCode, 0, "code", "", "code of the game (command line only)")
	gameAdd := &ff.Command{
		Name:      "add",
		Usage:     "fuingo [--db PATH] game add --manager EMAIL --code CODE",
		ShortHelp: "make an API account a manager of a game",
		Flags:     gameAddFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("game add takes no arguments: %w", ErrUsage)
			}
			if manager == "" {
				return ErrMissingManager
			}
			if addCode == "" {
				return ErrMissingCode
			}
			code, err := gameCode(addCode)
			if err != nil {
				return err
			}
			email, err := emailAddress(manager)
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				if err := s.AddManager(ctx, store.NewManager{GameCode: code, Email: email}); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "added manager %s to game %s\n", email, code)
				return nil
			})
		},
	}
	game := &ff.Command{
		Name:        "game",
		Usage:       "fuingo [--db PATH] game <create|add>",
		ShortHelp:   "manage games",
		Flags:       ff.NewFlagSet("game").SetParent(rootFlags),
		Subcommands: []*ff.Command{gameCreate, gameAdd},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	var token string
	uploaderCreateFlags := ff.NewFlagSet("uploader create").SetParent(rootFlags)
	uploaderCreateFlags.StringVar(&token, 0, "token", "", "upload token; a random UUID if omitted (command line only)")
	uploaderCreate := &ff.Command{
		Name:      "create",
		Usage:     "fuingo [--db PATH] uploader create [--token TOKEN] GAME EMAIL",
		ShortHelp: "create an active upload account, printing a generated token",
		Flags:     uploaderCreateFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("uploader create GAME EMAIL: %w", ErrUsage)
			}
			code, err := gameCode(args[0])
			if err != nil {
				return err
			}
			email, err := emailAddress(args[1])
			if err != nil {
				return err
			}
			generated := token == ""
			if generated {
				token = uuid.NewString()
			} else if !headerWord(token) {
				return ErrToken // never echo the token
			}
			pepper, err := cli.Pepper()
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				if err := s.CreateUploader(ctx, store.NewUploader{
					GameCode: code,
					Email:    email,
					CredentialHash: func(gameID int64) []byte {
						return credential.Hash(pepper, email, gameID, token)
					},
				}); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "created uploader %s in game %s\n", email, code)
				if generated {
					fmt.Fprintf(stdout, "token: %s\n", token)
				}
				return nil
			})
		},
	}
	uploader := &ff.Command{
		Name:        "uploader",
		Usage:       "fuingo [--db PATH] uploader <create>",
		ShortHelp:   "manage upload accounts",
		Flags:       ff.NewFlagSet("uploader").SetParent(rootFlags),
		Subcommands: []*ff.Command{uploaderCreate},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	var generatePassword bool
	accountCreateFlags := ff.NewFlagSet("account create").SetParent(rootFlags)
	accountCreateFlags.BoolVar(&generatePassword, 0, "generate-password", "generate and print a password instead of reading one from standard input (command line only)")
	accountCreate := &ff.Command{
		Name:      "create",
		Usage:     "fuingo [--db PATH] account create [--generate-password] EMAIL",
		ShortHelp: "create an API account",
		Flags:     accountCreateFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("account create EMAIL: %w", ErrUsage)
			}
			email, err := emailAddress(args[0])
			if err != nil {
				return err
			}
			var password string
			if generatePassword {
				password = rand.Text()
			} else if password, err = readPassword(stdin); err != nil {
				return err
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				id, err := s.CreateAccount(ctx, store.NewAccount{Email: email, PasswordHash: hash})
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "created account %s (id %d)\n", email, id)
				if generatePassword {
					fmt.Fprintf(stdout, "password: %s\n", password)
				}
				return nil
			})
		},
	}
	account := &ff.Command{
		Name:        "account",
		Usage:       "fuingo [--db PATH] account <create>",
		ShortHelp:   "manage API accounts",
		Flags:       ff.NewFlagSet("account").SetParent(rootFlags),
		Subcommands: []*ff.Command{accountCreate},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	root := &ff.Command{
		Name:        "fuingo",
		Usage:       "fuingo [--env ENV] [--db PATH] <version|game|uploader|account>",
		LongHelp:    "fuingo " + fuingo.Version().String(),
		Flags:       rootFlags,
		Subcommands: []*ff.Command{version, game, uploader, account},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	err := parse(root, args, &env, func() bool {
		return root.GetSelected() == version && !schema
	})
	if err == nil {
		err = root.Run(ctx)
	}
	if errors.Is(err, ff.ErrHelp) {
		fmt.Fprint(stderr, ffhelp.Command(root.GetSelected()))
	}
	return err
}

// parse parses args into root with cli.Parse, except that it loads no
// environment when the command line alone selects a command that needs none.
func parse(root *ff.Command, args []string, env *string, needsNoEnvironment func() bool) error {
	if err := root.Parse(args); err != nil || needsNoEnvironment() {
		return err
	}
	if err := root.Reset(); err != nil {
		return err
	}
	return cli.Parse(root, args, env, "schema", "token", "generate-password", "manager", "code")
}

// withDatabase opens the database at path, which must already be at the
// schema version this program expects, and runs fn on it.
func withDatabase(ctx context.Context, path string, fn func(*store.Store) error) (err error) {
	switch path {
	case "":
		return ErrMissingDatabase
	case ":memory:":
		return ErrMemoryDatabase
	}
	s, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	return fn(s)
}

// gameCode lowercases code and checks that it can appear in a turn file's
// header line.
func gameCode(code string) (string, error) {
	if !headerWord(code) {
		return "", fmt.Errorf("%q: %w", code, ErrGameCode)
	}
	return credential.NormalizeGameCode(code), nil
}

// emailAddress lowercases email and checks that it can appear in a turn
// file's header line.
func emailAddress(email string) (string, error) {
	if !headerWord(email) {
		return "", fmt.Errorf("%q: %w", email, ErrEmail)
	}
	return credential.NormalizeEmail(email), nil
}

// headerWord reports whether s is one or more bytes in 0x21-0x7E, as every
// word of a turn file's header line must be.
func headerWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// readPassword returns the first line of r, without its LF or CRLF ending.
// bcrypt refuses passwords over 72 bytes, so reading stops well past that.
func readPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 1024)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return "", ErrMissingPassword
	}
	return line, nil
}
