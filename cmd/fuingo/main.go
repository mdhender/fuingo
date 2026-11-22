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
//	fuingo [--env ENV] [--db PATH] [--public-url URL] account create EMAIL
//	fuingo [--env ENV] [--db PATH] [--public-url URL] account reset-password EMAIL
//	fuingo [--env ENV] [--db PATH] account (disable|enable) EMAIL
//	fuingo [--env ENV] [--db PATH] file add --system SYSTEM [--name NAME] PATH
//	fuingo [--env ENV] [--db PATH] file list --system SYSTEM
//	fuingo [--env ENV] [--db PATH] file withdraw --system SYSTEM ID
//
// Every flag can also be set with a FUINGO_ environment variable:
//
//	--env   FUINGO_ENV   development, test, production, or agent (required by every
//	                     command but version without --schema)
//	--db    FUINGO_DB    path to the database file (required by commands that use it;
//	                     :memory: is refused)
//	--public-url  FUINGO_PUBLIC_URL  the server's public origin, such as
//	                     https://fuingo.example.com, for the links in mail (required
//	                     by commands that send mail, except in development and agent)
//
// --schema, --manager, --player, --system, --code, and --name
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
// game. game add --player makes an account a player in it; if the email has
// no account, it creates one and mails its invitation.
//
// game disable and game enable clear or set is_active on the game, or with
// --player or --manager, on that player or manager of the game. Rows are
// never deleted. account disable and account enable clear or set is_active
// on an account; an inactive account manages no games and plays in none.
//
// No command sets or reads a password. account create makes an account and
// mails its invitation: a link, valid for 72 hours and usable once, with
// which the owner chooses a password. account reset-password mails such a
// link to an existing account; the password it has keeps working until the
// owner chooses a new one. Mail is sent through Mailgun, whose settings are
// secrets read only from the environment (FUINGO_MAILGUN_DOMAIN,
// FUINGO_MAILGUN_API_KEY, FUINGO_MAILGUN_FROM, and optionally
// FUINGO_MAILGUN_API_BASE and FUINGO_MAILGUN_SANDBOX_TO_ADDR). A command that
// would send mail fails, changing nothing, if they are not set.
//
// The file commands manage a system's own files, which every active player
// and manager of the system's games may read, and which only the
// administrator adds. file add stores the file at PATH under its base name,
// or under --name, and prints its ID. The file must pass the checks every
// upload does: a safe name, and text that is not empty. No size limit
// applies. file list prints the system's files, withdrawn ones included, in
// the order they arrived. file withdraw hides a file from the pages; its
// record and contents are kept, and nothing is deleted.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/mail"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

const (
	ErrMissingDatabase  = cerrs.Error("--db (FUINGO_DB) is required")
	ErrMemoryDatabase   = cerrs.Error("fuingo requires a database file, not :memory:")
	ErrMissingCode      = cerrs.Error("--code is required")
	ErrMissingSystem    = cerrs.Error("--system is required")
	ErrAddWhat          = cerrs.Error("game add needs exactly one of --manager and --player")
	ErrToggleWhat       = cerrs.Error("give at most one of --manager and --player")
	ErrInactiveAccount  = cerrs.Error("the account is disabled")
	ErrMissingPublicURL = cerrs.Error("--public-url (FUINGO_PUBLIC_URL) is required to mail a link")
	ErrUsage            = cerrs.Error("wrong number of arguments")
	ErrUploadID         = cerrs.Error("a file's ID is a whole number, 1 or more")
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
	var publicURL string
	rootFlags.StringVar(&publicURL, 0, "public-url", "", "the server's public origin, for the links in mail (FUINGO_PUBLIC_URL)")

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
			if addPlayer != "" {
				return withDatabase(ctx, path, func(s *store.Store) error {
					// An email with no account gets one, and its invitation:
					// make sure that can be mailed before changing anything.
					var l *links.Service
					if _, found, err := s.FindAccount(ctx, email); err != nil {
						return err
					} else if !found {
						if l, err = linkService(s, env, publicURL); err != nil {
							return fmt.Errorf("%s has no account, and its invitation cannot be sent: %w", email, err)
						}
					}
					added, err := s.AddPlayer(ctx, member)
					if err != nil {
						return err
					}
					fmt.Fprintf(stdout, "added player %s to game %s (id %d)\n", email, game, added.ID)
					if !added.NewAccount {
						return nil
					}
					if l == nil { // the account was created by someone else in between
						if l, err = linkService(s, env, publicURL); err != nil {
							return err
						}
					}
					return invite(ctx, l, stdout, added.AccountID, email)
				})
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
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

	accountCreate := &ff.Command{
		Name:      "create",
		Usage:     "fuingo [--db PATH] [--public-url URL] account create EMAIL",
		ShortHelp: "create an account and mail it an invitation",
		Flags:     ff.NewFlagSet("account create").SetParent(rootFlags),
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("account create EMAIL: %w", ErrUsage)
			}
			email, err := emailAddress(args[0])
			if err != nil {
				return err
			}
			return withLinks(ctx, path, env, publicURL, func(s *store.Store, l *links.Service) error {
				id, err := s.CreateAccount(ctx, store.NewAccount{Email: email})
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "created account %s (id %d)\n", email, id)
				return invite(ctx, l, stdout, id, email)
			})
		},
	}
	accountResetPassword := &ff.Command{
		Name:      "reset-password",
		Usage:     "fuingo [--db PATH] [--public-url URL] account reset-password EMAIL",
		ShortHelp: "mail an account a link to choose a new password",
		Flags:     ff.NewFlagSet("account reset-password").SetParent(rootFlags),
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("account reset-password EMAIL: %w", ErrUsage)
			}
			email, err := emailAddress(args[0])
			if err != nil {
				return err
			}
			return withLinks(ctx, path, env, publicURL, func(s *store.Store, l *links.Service) error {
				account, found, err := s.FindAccount(ctx, email)
				if err != nil {
					return err
				} else if !found {
					return fmt.Errorf("%s: %w", email, store.ErrUnknownAccount)
				} else if !account.Active {
					return fmt.Errorf("%s: %w", email, ErrInactiveAccount)
				}
				if _, err := l.SendTo(ctx, email); err != nil {
					return fmt.Errorf("the link could not be sent to %s: %w", email, err)
				}
				fmt.Fprintf(stdout, "mailed %s a link to choose a password\n", email)
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
		Usage:       "fuingo [--db PATH] account <create|reset-password|disable|enable>",
		ShortHelp:   "manage accounts",
		Flags:       ff.NewFlagSet("account").SetParent(rootFlags),
		Subcommands: []*ff.Command{accountCreate, accountResetPassword, accountToggle("disable", false), accountToggle("enable", true)},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	// systemCode checks the --system of a file command.
	systemCode := func(arg string) (string, error) {
		if arg == "" {
			return "", ErrMissingSystem
		}
		return code(arg)
	}
	var fileAddSystem, fileAddName string
	fileAddFlags := ff.NewFlagSet("file add").SetParent(rootFlags)
	fileAddFlags.StringVar(&fileAddSystem, 0, "system", "", "code of the system the file is for (command line only)")
	fileAddFlags.StringVar(&fileAddName, 0, "name", "", "name to store the file under; the default is PATH's base name (command line only)")
	fileAdd := &ff.Command{
		Name:      "add",
		Usage:     "fuingo [--db PATH] file add --system SYSTEM [--name NAME] PATH",
		ShortHelp: "add a file at a system's level",
		Flags:     fileAddFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("file add --system SYSTEM [--name NAME] PATH: %w", ErrUsage)
			}
			system, err := systemCode(fileAddSystem)
			if err != nil {
				return err
			}
			name := fileAddName
			if name == "" {
				name = filepath.Base(args[0])
			}
			if err := names.File(name); err != nil {
				return fmt.Errorf("%q: %w", name, err)
			}
			contents, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			if err := names.Text(contents); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				u, err := s.CreateSystemUpload(ctx, system, name, contents, time.Now())
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "added %s to system %s (id %d, %d bytes)\n", u.Name, system, u.ID, u.Size)
				return nil
			})
		},
	}
	var fileListSystem string
	fileListFlags := ff.NewFlagSet("file list").SetParent(rootFlags)
	fileListFlags.StringVar(&fileListSystem, 0, "system", "", "code of the system (command line only)")
	fileList := &ff.Command{
		Name:      "list",
		Usage:     "fuingo [--db PATH] file list --system SYSTEM",
		ShortHelp: "list a system's own files, withdrawn ones included",
		Flags:     fileListFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("file list --system SYSTEM: %w", ErrUsage)
			}
			system, err := systemCode(fileListSystem)
			if err != nil {
				return err
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				uploads, err := s.SystemUploads(ctx, system, true)
				if err != nil {
					return err
				}
				for _, u := range uploads {
					fmt.Fprintf(stdout, "%d\t%s\t%d\t%s", u.ID, u.Name, u.Size, u.UploadedAt.UTC().Format(time.RFC3339))
					if u.Withdrawn() {
						fmt.Fprintf(stdout, "\twithdrawn %s", u.WithdrawnAt.UTC().Format(time.RFC3339))
					}
					fmt.Fprintln(stdout)
				}
				return nil
			})
		},
	}
	var fileWithdrawSystem string
	fileWithdrawFlags := ff.NewFlagSet("file withdraw").SetParent(rootFlags)
	fileWithdrawFlags.StringVar(&fileWithdrawSystem, 0, "system", "", "code of the system (command line only)")
	fileWithdraw := &ff.Command{
		Name:      "withdraw",
		Usage:     "fuingo [--db PATH] file withdraw --system SYSTEM ID",
		ShortHelp: "withdraw one of a system's own files; nothing is deleted",
		Flags:     fileWithdrawFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("file withdraw --system SYSTEM ID: %w", ErrUsage)
			}
			system, err := systemCode(fileWithdrawSystem)
			if err != nil {
				return err
			}
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id < 1 {
				return fmt.Errorf("%q: %w", args[0], ErrUploadID)
			}
			return withDatabase(ctx, path, func(s *store.Store) error {
				u, err := s.WithdrawSystemUpload(ctx, system, id, time.Now())
				if err != nil {
					return fmt.Errorf("system %s, file %d: %w", system, id, err)
				}
				fmt.Fprintf(stdout, "withdrew %s from system %s (id %d)\n", u.Name, system, u.ID)
				return nil
			})
		},
	}
	file := &ff.Command{
		Name:        "file",
		Usage:       "fuingo [--db PATH] file <add|list|withdraw>",
		ShortHelp:   "manage a system's own files",
		Flags:       ff.NewFlagSet("file").SetParent(rootFlags),
		Subcommands: []*ff.Command{fileAdd, fileList, fileWithdraw},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	root := &ff.Command{
		Name:        "fuingo",
		Usage:       "fuingo [--env ENV] [--db PATH] <version|system|game|account|file>",
		LongHelp:    "fuingo " + fuingo.Version().String(),
		Flags:       rootFlags,
		Subcommands: []*ff.Command{version, system, game, account, file},
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
	return cli.Parse(root, args, env, "schema", "manager", "player", "system", "code", "name")
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

// newSender returns the mail sender for the environment's settings. Tests
// replace it.
var newSender = mail.SenderFromEnv

// linkService returns the service that mails links, or fails if this
// program is not configured to: it needs the Mailgun settings and the
// server's public origin.
func linkService(s *store.Store, env, publicURL string) (*links.Service, error) {
	sender, err := newSender(env)
	if err != nil {
		return nil, err
	}
	if _, unconfigured := sender.(mail.Unconfigured); unconfigured {
		return nil, mail.ErrNotConfigured
	}
	if publicURL == "" {
		if publicURL = links.DefaultPublicURL(env); publicURL == "" {
			return nil, ErrMissingPublicURL
		}
	}
	if publicURL, err = links.PublicURL(publicURL); err != nil {
		return nil, fmt.Errorf("--public-url %q: %w", publicURL, err)
	}
	return &links.Service{Store: s, Sender: sender, PublicURL: publicURL}, nil
}

// withLinks is withDatabase for a command that mails a link. It fails
// before running fn if the link could not be mailed.
func withLinks(ctx context.Context, path, env, publicURL string, fn func(*store.Store, *links.Service) error) error {
	return withDatabase(ctx, path, func(s *store.Store) error {
		l, err := linkService(s, env, publicURL)
		if err != nil {
			return err
		}
		return fn(s, l)
	})
}

// invite mails a new account its invitation. The account stays if the mail
// fails; the error says how to send another.
func invite(ctx context.Context, l *links.Service, stdout io.Writer, accountID int64, email string) error {
	if err := l.Send(ctx, accountID, email, store.LinkInvitation); err != nil {
		return fmt.Errorf("the invitation could not be sent to %s: %w (send another with: fuingo account reset-password %s)", email, err, email)
	}
	fmt.Fprintf(stdout, "mailed %s an invitation\n", email)
	return nil
}
