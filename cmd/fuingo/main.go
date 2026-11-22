// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command fuingo is the administrator's CLI for the application data. Its
// commands follow the pattern fuingo <noun> <verb>.
//
//	fuingo version
//	fuingo [--env ENV] [--db PATH] version --schema
//	fuingo [--env ENV] [--db PATH] system create CODE
//	fuingo [--env ENV] [--db PATH] game create --system SYSTEM CODE
//	fuingo [--env ENV] [--db PATH] game add (--manager EMAIL | --player EMAIL) --system SYSTEM --code CODE
//	fuingo [--env ENV] [--db PATH] game (disable|enable) [--player EMAIL | --manager EMAIL] --system SYSTEM --code CODE
//	fuingo [--env ENV] [--db PATH] account create [--generate-password] EMAIL
//	fuingo [--env ENV] [--db PATH] account set-password [--generate-password] EMAIL
//	fuingo [--env ENV] [--db PATH] account (disable|enable) EMAIL
//
// Every flag can also be set with a FUINGO_ environment variable:
//
//	--env   FUINGO_ENV   development, test, production, or agent (required by every
//	                     command but version without --schema)
//	--db    FUINGO_DB    path to the database file (required by commands that use it;
//	                     :memory: is refused)
//
// --schema, --generate-password, --manager, --player, --system, and --code
// are honored only on the command line; their FUINGO_ variables are ignored.
// version reads no environment unless --schema is on the command line.
//
// Commands that use the database only open one that is already at the schema
// version this program expects. fuingo never creates a database file or
// migrates one; fuingodb does.
//
// A system is a game such as Olympia; a game is one instance of it, such as
// Olympia / G3, whose code is unique only within its system. Codes and
// emails are lowercased with strings.ToLower. A code is 1 to 32 letters,
// digits, hyphens, or underscores; an email is one or more printable ASCII
// bytes with no spaces. New games, players, and managers are active.
//
// Each email has one account, for playing and managing alike.
// game add --manager makes an existing account a manager of an existing
// game. game add --player makes an account a player in it, creating the
// account without a password if it does not exist.
//
// game disable and game enable clear or set is_active on the game, or with
// --player or --manager, on that player or manager of the game. Rows are
// never deleted. account disable and account enable clear or set is_active
// on an account; an inactive account manages no games and plays in none.
// account create makes an account with a password, and account set-password
// sets or replaces the password of an existing account, such as one made by
// game add --player. Each reads the password from the first line of standard
// input, or, with --generate-password, generates one and prints it once.
// Passwords are hashed with bcrypt at bcrypt.MinCost and are at most 72
// bytes.
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

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
	"golang.org/x/crypto/bcrypt"
)

const (
	ErrMissingDatabase = cerrs.Error("--db (FUINGO_DB) is required")
	ErrMemoryDatabase  = cerrs.Error("fuingo requires a database file, not :memory:")
	ErrMissingCode     = cerrs.Error("--code is required")
	ErrMissingSystem   = cerrs.Error("--system is required")
	ErrAddWhat         = cerrs.Error("game add needs exactly one of --manager and --player")
	ErrToggleWhat      = cerrs.Error("give at most one of --manager and --player")
	ErrMissingPassword = cerrs.Error("no password on standard input; pipe one in or use --generate-password")
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

	systemCreate := &ff.Command{
		Name:      "create",
		Usage:     "fuingo [--db PATH] system create CODE",
		ShortHelp: "create a system",
		Flags:     ff.NewFlagSet("system create").SetParent(rootFlags),
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("system create CODE: %w", ErrUsage)
			}
			code, err := code(args[0])
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				id, err := s.CreateSystem(ctx, code)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "created system %s (id %d)\n", code, id)
				return nil
			})
		},
	}
	system := &ff.Command{
		Name:        "system",
		Usage:       "fuingo [--db PATH] system <create>",
		ShortHelp:   "manage systems",
		Flags:       ff.NewFlagSet("system").SetParent(rootFlags),
		Subcommands: []*ff.Command{systemCreate},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	var createSystem string
	gameCreateFlags := ff.NewFlagSet("game create").SetParent(rootFlags)
	gameCreateFlags.StringVar(&createSystem, 0, "system", "", "code of the game's system (command line only)")
	gameCreate := &ff.Command{
		Name:      "create",
		Usage:     "fuingo [--db PATH] game create --system SYSTEM CODE",
		ShortHelp: "create an active game in a system",
		Flags:     gameCreateFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("game create --system SYSTEM CODE: %w", ErrUsage)
			}
			game, err := gameRef(createSystem, args[0])
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				id, err := s.CreateGame(ctx, game)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "created game %s (id %d)\n", game, id)
				return nil
			})
		},
	}
	var manager, addPlayer, addSystem, addCode string
	gameAddFlags := ff.NewFlagSet("game add").SetParent(rootFlags)
	gameAddFlags.StringVar(&manager, 0, "manager", "", "email of the account to make a manager (command line only)")
	gameAddFlags.StringVar(&addPlayer, 0, "player", "", "email of the account to make a player (command line only)")
	gameAddFlags.StringVar(&addSystem, 0, "system", "", "code of the game's system (command line only)")
	gameAddFlags.StringVar(&addCode, 0, "code", "", "code of the game (command line only)")
	gameAdd := &ff.Command{
		Name:      "add",
		Usage:     "fuingo [--db PATH] game add (--manager EMAIL | --player EMAIL) --system SYSTEM --code CODE",
		ShortHelp: "make an account a manager of a game, or a player in it",
		Flags:     gameAddFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("game add takes no arguments: %w", ErrUsage)
			}
			if (manager == "") == (addPlayer == "") {
				return ErrAddWhat
			}
			game, err := gameRef(addSystem, addCode)
			if err != nil {
				return err
			}
			email, err := emailAddress(manager + addPlayer)
			if err != nil {
				return err
			}
			member := store.Member{Game: game, Email: email}
			return withDatabase(ctx, path, func(s *store.Store) error {
				if addPlayer != "" {
					id, err := s.AddPlayer(ctx, member)
					if err != nil {
						return err
					}
					fmt.Fprintf(stdout, "added player %s to game %s (id %d)\n", email, game, id)
					return nil
				}
				if err := s.AddManager(ctx, member); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "added manager %s to game %s\n", email, game)
				return nil
			})
		},
	}
	// toggle builds game disable (active false) and game enable (active true).
	toggle := func(name string, active bool) *ff.Command {
		var manager, player, systemArg, codeArg string
		flags := ff.NewFlagSet("game " + name).SetParent(rootFlags)
		flags.StringVar(&player, 0, "player", "", "email of the player to "+name+" (command line only)")
		flags.StringVar(&manager, 0, "manager", "", "email of the manager to "+name+" (command line only)")
		flags.StringVar(&systemArg, 0, "system", "", "code of the game's system (command line only)")
		flags.StringVar(&codeArg, 0, "code", "", "code of the game (command line only)")
		return &ff.Command{
			Name:      name,
			Usage:     "fuingo [--db PATH] game " + name + " [--player EMAIL | --manager EMAIL] --system SYSTEM --code CODE",
			ShortHelp: name + " a game, or a player or manager of it",
			Flags:     flags,
			Exec: func(ctx context.Context, args []string) error {
				if len(args) != 0 {
					return fmt.Errorf("game %s takes no arguments: %w", name, ErrUsage)
				}
				if manager != "" && player != "" {
					return ErrToggleWhat
				}
				game, err := gameRef(systemArg, codeArg)
				if err != nil {
					return err
				}
				var email string
				if manager != "" || player != "" {
					if email, err = emailAddress(manager + player); err != nil {
						return err
					}
				}
				member := store.Member{Game: game, Email: email}
				return withDatabase(ctx, path, func(s *store.Store) error {
					switch {
					case player != "":
						if err := s.SetPlayerActive(ctx, member, active); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "%sd player %s in game %s\n", name, email, game)
					case manager != "":
						if err := s.SetManagerActive(ctx, member, active); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "%sd manager %s of game %s\n", name, email, game)
					default:
						if err := s.SetGameActive(ctx, game, active); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "%sd game %s\n", name, game)
					}
					return nil
				})
			},
		}
	}

	game := &ff.Command{
		Name:        "game",
		Usage:       "fuingo [--db PATH] game <create|add|disable|enable>",
		ShortHelp:   "manage games",
		Flags:       ff.NewFlagSet("game").SetParent(rootFlags),
		Subcommands: []*ff.Command{gameCreate, gameAdd, toggle("disable", false), toggle("enable", true)},
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
		ShortHelp: "create an account with a password",
		Flags:     accountCreateFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("account create EMAIL: %w", ErrUsage)
			}
			email, err := emailAddress(args[0])
			if err != nil {
				return err
			}
			password, hash, err := newPassword(stdin, generatePassword)
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				id, err := s.CreateAccount(ctx, store.NewAccount{Email: email, HashedSecret: hash})
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
	var setGeneratePassword bool
	accountSetPasswordFlags := ff.NewFlagSet("account set-password").SetParent(rootFlags)
	accountSetPasswordFlags.BoolVar(&setGeneratePassword, 0, "generate-password", "generate and print a password instead of reading one from standard input (command line only)")
	accountSetPassword := &ff.Command{
		Name:      "set-password",
		Usage:     "fuingo [--db PATH] account set-password [--generate-password] EMAIL",
		ShortHelp: "set or replace an account's password",
		Flags:     accountSetPasswordFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("account set-password EMAIL: %w", ErrUsage)
			}
			email, err := emailAddress(args[0])
			if err != nil {
				return err
			}
			password, hash, err := newPassword(stdin, setGeneratePassword)
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				if err := s.SetAccountSecret(ctx, email, hash); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "set the password of account %s\n", email)
				if setGeneratePassword {
					fmt.Fprintf(stdout, "password: %s\n", password)
				}
				return nil
			})
		},
	}
	// accountToggle builds account disable (active false) and account enable
	// (active true).
	accountToggle := func(name string, active bool) *ff.Command {
		return &ff.Command{
			Name:      name,
			Usage:     "fuingo [--db PATH] account " + name + " EMAIL",
			ShortHelp: name + " an account",
			Flags:     ff.NewFlagSet("account " + name).SetParent(rootFlags),
			Exec: func(ctx context.Context, args []string) error {
				if len(args) != 1 {
					return fmt.Errorf("account %s EMAIL: %w", name, ErrUsage)
				}
				email, err := emailAddress(args[0])
				if err != nil {
					return err
				}
				return withDatabase(ctx, path, func(s *store.Store) error {
					if err := s.SetAccountActive(ctx, email, active); err != nil {
						return err
					}
					fmt.Fprintf(stdout, "%sd account %s\n", name, email)
					return nil
				})
			},
		}
	}
	account := &ff.Command{
		Name:        "account",
		Usage:       "fuingo [--db PATH] account <create|set-password|disable|enable>",
		ShortHelp:   "manage accounts",
		Flags:       ff.NewFlagSet("account").SetParent(rootFlags),
		Subcommands: []*ff.Command{accountCreate, accountSetPassword, accountToggle("disable", false), accountToggle("enable", true)},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	root := &ff.Command{
		Name:        "fuingo",
		Usage:       "fuingo [--env ENV] [--db PATH] <version|system|game|account>",
		LongHelp:    "fuingo " + fuingo.Version().String(),
		Flags:       rootFlags,
		Subcommands: []*ff.Command{version, system, game, account},
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
	return cli.Parse(root, args, env, "schema", "generate-password", "manager", "player", "system", "code")
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

// code lowercases the code of a system or game and checks it.
func code(arg string) (string, error) {
	c, err := names.Code(arg)
	if err != nil {
		return "", fmt.Errorf("%q: %w", arg, err)
	}
	return c, nil
}

// gameRef checks and lowercases the codes naming a game: its system's, from
// --system, and its own.
func gameRef(systemArg, codeArg string) (store.GameRef, error) {
	if systemArg == "" {
		return store.GameRef{}, ErrMissingSystem
	}
	if codeArg == "" {
		return store.GameRef{}, ErrMissingCode
	}
	system, err := code(systemArg)
	if err != nil {
		return store.GameRef{}, err
	}
	game, err := code(codeArg)
	if err != nil {
		return store.GameRef{}, err
	}
	return store.GameRef{System: system, Code: game}, nil
}

// emailAddress lowercases email and checks it.
func emailAddress(email string) (string, error) {
	e, err := names.Email(email)
	if err != nil {
		return "", fmt.Errorf("%q: %w", email, err)
	}
	return e, nil
}

// newPassword generates a password, or reads one from stdin, and returns it
// with its bcrypt hash at bcrypt.MinCost.
func newPassword(stdin io.Reader, generate bool) (password string, hash []byte, err error) {
	if generate {
		password = rand.Text()
	} else if password, err = readPassword(stdin); err != nil {
		return "", nil, err
	}
	hash, err = bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		return "", nil, err
	}
	return password, hash, nil
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
