// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command olympia is the game master's tool for Olympia G3 games whose
// players upload their orders to fuingo. Its commands follow the pattern
// olympia <noun> <verb>.
//
//	olympia version
//	olympia [--env ENV] [--store PATH] store (create|migrate)
//	olympia [--env ENV] [--store PATH] game add CODE
//	olympia [--env ENV] [--store PATH] game (list | show CODE)
//	olympia [--env ENV] [--store PATH] game rename CODE NEW-CODE
//	olympia [--env ENV] [--store PATH] game (disable|enable|delete) CODE
//	olympia [--env ENV] [--store PATH] player add --game CODE --faction ID --uploader EMAIL [--name NAME]
//	olympia [--env ENV] [--store PATH] player list --game CODE
//	olympia [--env ENV] [--store PATH] player show --game CODE --faction ID
//	olympia [--env ENV] [--store PATH] player update --game CODE --faction ID
//	        [--new-faction ID] [--uploader EMAIL] [--name NAME]
//	olympia [--env ENV] [--store PATH] player (disable|enable|delete) --game CODE --faction ID
//	olympia [--env ENV] stage [--server URL] [--email EMAIL] [--spool DIR] [--batch DIR]
//	        --game CODE --turn N [--rebuild] [--force]
//
// # The store
//
// The store is olympia's own SQLite file, separate from fuingo's database,
// holding the games the game master runs and their players. store create
// makes a new one and fails if the file exists; store migrate brings an
// older one up to date. Every other command that uses it opens an existing,
// current store and never creates one.
//
// A game's code is its code in fuingo. A player is an Olympia G3 faction,
// the player entity code on its BEGIN line, such as ab1, played by the
// fuingo uploader with the given email. One uploader may play several
// factions. Codes, factions, and emails are lowercased.
//
// disable and enable clear or set a game's or player's active flag, and
// repeating one succeeds. delete removes a game only while it has no
// players, and a player only while nothing refers to it; disable it
// otherwise.
//
// # Staging
//
// stage downloads every upload for a game's turn from fuingo's /api/ and
// writes each one, in upload order, to the engine's spool directory as an
// mbox-style message named m<turn>-<seq>-<email>-<upload id>. The engine
// reads each spool file, applies its orders, and deletes it. It does not use
// the store.
//
// The downloads are first kept in a batch directory,
// <batch>/<game>/turn-<turn>/, with a manifest.json listing each staged
// file's name, uploader email, upload time, content hash, and upload ID.
// The batch appears only once every download has been checked against its
// hash. Staging the turn again reuses the batch, ignoring later uploads, so
// a retry stages the same orders; --rebuild downloads it again.
// stage refuses a spool directory that already holds files whose names
// start with "m", which the engine would read as orders, unless --force is
// given.
//
// # Configuration
//
// These flags can also be set with an OLYMPIA_ environment variable, read
// from the process environment or from the environment files that --env
// selects (see internal/dotenv). Explicit flags override the environment,
// which overrides the defaults:
//
//	--env     OLYMPIA_ENV     development, test, production, or agent (required by every
//	                          command but version)
//	--store   OLYMPIA_STORE   olympia's store file (required by store, game, and player)
//	--server  OLYMPIA_SERVER  fuingo's URL, such as https://fuingo.example.com
//	--email   OLYMPIA_EMAIL   the email of a fuingo account that manages the game
//	--spool   OLYMPIA_SPOOL   the engine's spool directory, <libdir>/spool
//	--batch   OLYMPIA_BATCH   the directory that holds the batches
//
// OLYMPIA_PASSWORD is the account's API password. It has no flag: set it in
// the process environment or an ignored .local environment file. olympia
// never writes it, or the bearer token it logs in for, to any file.
//
// --game, --turn, --rebuild, --force, --faction, --new-faction, --uploader,
// and --name are honored only on the command line; their OLYMPIA_ variables
// are ignored.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/gmstore"
	"github.com/mdhender/fuingo/internal/olympia"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

// EnvPrefix prefixes olympia's environment variables.
const EnvPrefix = "OLYMPIA"

const (
	ErrMissingPassword = cerrs.Error("OLYMPIA_PASSWORD is missing or empty")
	ErrMissingStore    = cerrs.Error("--store (OLYMPIA_STORE) is required")
	ErrMemoryStore     = cerrs.Error("olympia requires a store file, not :memory:")
	ErrUsage           = cerrs.Error("wrong number of arguments")
	ErrGameCode        = cerrs.Error("game code must be lowercase letters, digits, '.', '_', or '-', starting with a letter or digit")
	ErrFaction         = cerrs.Error("faction must be an Olympia G3 entity code, such as ab1, a12, ab12, or 1234")
	ErrEmail           = cerrs.Error("uploader email must be one or more printable ASCII bytes with no spaces")
	ErrName            = cerrs.Error("name must be at most 128 printable characters")
	ErrNoChange        = cerrs.Error("give at least one of --new-faction, --uploader, and --name")
)

// commandLineOnly lists the flags whose environment variables are ignored.
var commandLineOnly = []string{"game", "turn", "rebuild", "force", "faction", "new-faction", "uploader", "name"}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, ff.ErrHelp) {
			fmt.Fprintf(os.Stderr, "olympia: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var env, storePath string
	rootFlags := ff.NewFlagSet("olympia")
	rootFlags.StringVar(&env, 0, "env", "", "environment: development, test, production, or agent (OLYMPIA_ENV)")
	rootFlags.StringVar(&storePath, 0, "store", "", "olympia's store file (OLYMPIA_STORE)")

	withStore := func(fn func(*gmstore.Store) error) error {
		if err := checkStorePath(storePath); err != nil {
			return err
		}
		s, err := gmstore.Open(ctx, storePath)
		if err != nil {
			return err
		}
		defer s.Close()
		return fn(s)
	}

	version := &ff.Command{
		Name:      "version",
		Usage:     "olympia version",
		ShortHelp: "print the program's version",
		Flags:     ff.NewFlagSet("version").SetParent(rootFlags),
		Exec: func(ctx context.Context, args []string) error {
			fmt.Fprintln(stdout, fuingo.Version().String())
			return nil
		},
	}

	storeCmd := &ff.Command{
		Name:      "store",
		Usage:     "olympia [--store PATH] store (create|migrate)",
		ShortHelp: "create or migrate olympia's store file",
		Flags:     ff.NewFlagSet("store").SetParent(rootFlags),
		Subcommands: []*ff.Command{
			{
				Name:      "create",
				Usage:     "olympia --store PATH store create",
				ShortHelp: "create a new, empty store; fails if the file exists",
				Flags:     ff.NewFlagSet("store create").SetParent(rootFlags),
				Exec: func(ctx context.Context, args []string) error {
					if len(args) != 0 {
						return ErrUsage
					}
					if err := checkStorePath(storePath); err != nil {
						return err
					}
					s, err := gmstore.Create(ctx, storePath)
					if err != nil {
						return err
					}
					fmt.Fprintf(stdout, "created %s\n", storePath)
					return s.Close()
				},
			},
			{
				Name:      "migrate",
				Usage:     "olympia --store PATH store migrate",
				ShortHelp: "bring an existing store up to the current schema",
				Flags:     ff.NewFlagSet("store migrate").SetParent(rootFlags),
				Exec: func(ctx context.Context, args []string) error {
					if len(args) != 0 {
						return ErrUsage
					}
					if err := checkStorePath(storePath); err != nil {
						return err
					}
					if err := gmstore.Migrate(ctx, storePath); err != nil {
						return err
					}
					fmt.Fprintf(stdout, "migrated %s to schema version %d\n", storePath, gmstore.CurrentSchemaVersion())
					return nil
				},
			},
		},
	}

	gameCmd := gameCommands(rootFlags, stdout, withStore)
	playerCmd := playerCommands(rootFlags, stdout, withStore)
	stage := stageCommand(rootFlags, stdout)

	root := &ff.Command{
		Name:        "olympia",
		Usage:       "olympia [--env ENV] [--store PATH] <command> ...",
		LongHelp:    "olympia " + fuingo.Version().String(),
		Flags:       rootFlags,
		Subcommands: []*ff.Command{version, storeCmd, gameCmd, playerCmd, stage},
	}
	// version reads no environment files, so it needs no --env.
	if len(args) > 0 && args[0] == "version" {
		if err := root.Parse(args); err != nil {
			return helpOr(err, root, stderr)
		}
		return root.Run(ctx)
	}
	if err := cli.ParsePrefix(EnvPrefix, root, args, &env, commandLineOnly...); err != nil {
		return helpOr(err, root, stderr)
	}
	if selected := root.GetSelected(); selected.Exec == nil {
		fmt.Fprint(stderr, ffhelp.Command(selected))
		return ff.ErrHelp
	}
	return root.Run(ctx)
}

func checkStorePath(path string) error {
	switch path {
	case "":
		return ErrMissingStore
	case ":memory:":
		return ErrMemoryStore
	}
	return nil
}

func helpOr(err error, root *ff.Command, stderr io.Writer) error {
	if errors.Is(err, ff.ErrHelp) {
		fmt.Fprint(stderr, ffhelp.Command(root.GetSelected()))
	}
	return err
}

// gameCode lowercases and checks a game code.
func gameCode(code string) (string, error) {
	code = credential.NormalizeGameCode(code)
	if !olympia.ValidGame(code) {
		return "", fmt.Errorf("%q: %w", code, ErrGameCode)
	}
	return code, nil
}

// faction lowercases and checks a faction.
func faction(id string) (string, error) {
	id = strings.ToLower(id)
	if !olympia.ValidFaction(id) {
		return "", fmt.Errorf("%q: %w", id, ErrFaction)
	}
	return id, nil
}

// uploaderEmail lowercases an email and checks that it could appear on an
// upload's header line.
func uploaderEmail(email string) (string, error) {
	if email == "" {
		return "", ErrEmail
	}
	for i := 0; i < len(email); i++ {
		if email[i] < 0x21 || email[i] > 0x7e {
			return "", ErrEmail
		}
	}
	return credential.NormalizeEmail(email), nil
}

// playerName checks a player's display name.
func playerName(name string) (string, error) {
	if len(name) > 128 {
		return "", ErrName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", ErrName
		}
	}
	return name, nil
}

func activeText(active bool) string {
	if active {
		return "active"
	}
	return "disabled"
}

func gameCommands(rootFlags *ff.FlagSet, stdout io.Writer, withStore func(func(*gmstore.Store) error) error) *ff.Command {
	oneCode := func(args []string) (string, error) {
		if len(args) != 1 {
			return "", ErrUsage
		}
		return gameCode(args[0])
	}
	toggle := func(name string, active bool) *ff.Command {
		return &ff.Command{
			Name:      name,
			Usage:     "olympia [--store PATH] game " + name + " CODE",
			ShortHelp: name + " a game",
			Flags:     ff.NewFlagSet("game " + name).SetParent(rootFlags),
			Exec: func(ctx context.Context, args []string) error {
				code, err := oneCode(args)
				if err != nil {
					return err
				}
				return withStore(func(s *gmstore.Store) error {
					if err := s.SetGameActive(ctx, code, active); err != nil {
						return err
					}
					fmt.Fprintf(stdout, "%sd game %s\n", name, code)
					return nil
				})
			},
		}
	}
	return &ff.Command{
		Name:      "game",
		Usage:     "olympia [--store PATH] game <verb> ...",
		ShortHelp: "manage the games in the store",
		Flags:     ff.NewFlagSet("game").SetParent(rootFlags),
		Subcommands: []*ff.Command{
			{
				Name:      "add",
				Usage:     "olympia [--store PATH] game add CODE",
				ShortHelp: "add an active game; CODE is its code in fuingo",
				Flags:     ff.NewFlagSet("game add").SetParent(rootFlags),
				Exec: func(ctx context.Context, args []string) error {
					code, err := oneCode(args)
					if err != nil {
						return err
					}
					return withStore(func(s *gmstore.Store) error {
						if err := s.AddGame(ctx, code); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "added game %s\n", code)
						return nil
					})
				},
			},
			{
				Name:      "list",
				Usage:     "olympia [--store PATH] game list",
				ShortHelp: "list the games",
				Flags:     ff.NewFlagSet("game list").SetParent(rootFlags),
				Exec: func(ctx context.Context, args []string) error {
					if len(args) != 0 {
						return ErrUsage
					}
					return withStore(func(s *gmstore.Store) error {
						games, err := s.Games(ctx)
						if err != nil {
							return err
						}
						tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
						fmt.Fprintln(tw, "GAME\tSTATUS\tPLAYERS")
						for _, g := range games {
							fmt.Fprintf(tw, "%s\t%s\t%d\n", g.Code, activeText(g.Active), g.Players)
						}
						return tw.Flush()
					})
				},
			},
			{
				Name:      "show",
				Usage:     "olympia [--store PATH] game show CODE",
				ShortHelp: "show a game",
				Flags:     ff.NewFlagSet("game show").SetParent(rootFlags),
				Exec: func(ctx context.Context, args []string) error {
					code, err := oneCode(args)
					if err != nil {
						return err
					}
					return withStore(func(s *gmstore.Store) error {
						g, err := s.Game(ctx, code)
						if err != nil {
							return err
						}
						fmt.Fprintf(stdout, "game:    %s\nstatus:  %s\nplayers: %d\n", g.Code, activeText(g.Active), g.Players)
						return nil
					})
				},
			},
			{
				Name:      "rename",
				Usage:     "olympia [--store PATH] game rename CODE NEW-CODE",
				ShortHelp: "change a game's code, as when it changes in fuingo",
				Flags:     ff.NewFlagSet("game rename").SetParent(rootFlags),
				Exec: func(ctx context.Context, args []string) error {
					if len(args) != 2 {
						return ErrUsage
					}
					code, err := gameCode(args[0])
					if err != nil {
						return err
					}
					newCode, err := gameCode(args[1])
					if err != nil {
						return err
					}
					return withStore(func(s *gmstore.Store) error {
						if err := s.RenameGame(ctx, code, newCode); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "renamed game %s to %s\n", code, newCode)
						return nil
					})
				},
			},
			toggle("disable", false),
			toggle("enable", true),
			{
				Name:      "delete",
				Usage:     "olympia [--store PATH] game delete CODE",
				ShortHelp: "delete a game that has no players",
				Flags:     ff.NewFlagSet("game delete").SetParent(rootFlags),
				Exec: func(ctx context.Context, args []string) error {
					code, err := oneCode(args)
					if err != nil {
						return err
					}
					return withStore(func(s *gmstore.Store) error {
						if err := s.DeleteGame(ctx, code); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "deleted game %s\n", code)
						return nil
					})
				},
			},
		},
	}
}

func playerCommands(rootFlags *ff.FlagSet, stdout io.Writer, withStore func(func(*gmstore.Store) error) error) *ff.Command {
	// target adds --game and --faction to a flag set and returns a function
	// that checks them.
	target := func(fs *ff.FlagSet) func(args []string) (string, string, error) {
		var game, fac string
		fs.StringVar(&game, 0, "game", "", "game code (command line only)")
		fs.StringVar(&fac, 0, "faction", "", "the player's Olympia G3 entity code, such as ab1 (command line only)")
		return func(args []string) (string, string, error) {
			if len(args) != 0 {
				return "", "", ErrUsage
			}
			if game == "" || fac == "" {
				return "", "", errors.New("--game and --faction are required")
			}
			code, err := gameCode(game)
			if err != nil {
				return "", "", err
			}
			f, err := faction(fac)
			return code, f, err
		}
	}
	toggle := func(name string, active bool) *ff.Command {
		fs := ff.NewFlagSet("player " + name).SetParent(rootFlags)
		get := target(fs)
		return &ff.Command{
			Name:      name,
			Usage:     "olympia [--store PATH] player " + name + " --game CODE --faction ID",
			ShortHelp: name + " a player",
			Flags:     fs,
			Exec: func(ctx context.Context, args []string) error {
				code, f, err := get(args)
				if err != nil {
					return err
				}
				return withStore(func(s *gmstore.Store) error {
					if err := s.SetPlayerActive(ctx, code, f, active); err != nil {
						return err
					}
					fmt.Fprintf(stdout, "%sd player %s in game %s\n", name, f, code)
					return nil
				})
			},
		}
	}

	addFlags := ff.NewFlagSet("player add").SetParent(rootFlags)
	addTarget := target(addFlags)
	var addEmail, addName string
	addFlags.StringVar(&addEmail, 0, "uploader", "", "the fuingo uploader's email (command line only)")
	addFlags.StringVar(&addName, 0, "name", "", "the player's name (command line only)")

	listFlags := ff.NewFlagSet("player list").SetParent(rootFlags)
	var listGame string
	listFlags.StringVar(&listGame, 0, "game", "", "game code (command line only)")

	showFlags := ff.NewFlagSet("player show").SetParent(rootFlags)
	showTarget := target(showFlags)

	updateFlags := ff.NewFlagSet("player update").SetParent(rootFlags)
	updateTarget := target(updateFlags)
	var newFaction, newEmail, newName string
	updateFlags.StringVar(&newFaction, 0, "new-faction", "", "the player's new entity code (command line only)")
	updateFlags.StringVar(&newEmail, 0, "uploader", "", "the new uploader's email (command line only)")
	updateFlags.StringVar(&newName, 0, "name", "", "the player's new name; empty clears it (command line only)")

	deleteFlags := ff.NewFlagSet("player delete").SetParent(rootFlags)
	deleteTarget := target(deleteFlags)

	isSet := func(fs *ff.FlagSet, name string) bool {
		f, ok := fs.GetFlag(name)
		return ok && f.IsSet()
	}

	return &ff.Command{
		Name:      "player",
		Usage:     "olympia [--store PATH] player <verb> ...",
		ShortHelp: "manage a game's players: Olympia G3 factions and their fuingo uploaders",
		Flags:     ff.NewFlagSet("player").SetParent(rootFlags),
		Subcommands: []*ff.Command{
			{
				Name:      "add",
				Usage:     "olympia [--store PATH] player add --game CODE --faction ID --uploader EMAIL [--name NAME]",
				ShortHelp: "add an active player to a game",
				Flags:     addFlags,
				Exec: func(ctx context.Context, args []string) error {
					code, f, err := addTarget(args)
					if err != nil {
						return err
					}
					email, err := uploaderEmail(addEmail)
					if err != nil {
						return err
					}
					name, err := playerName(addName)
					if err != nil {
						return err
					}
					return withStore(func(s *gmstore.Store) error {
						if err := s.AddPlayer(ctx, gmstore.Player{GameCode: code, Faction: f, Email: email, Name: name}); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "added player %s (%s) to game %s\n", f, email, code)
						return nil
					})
				},
			},
			{
				Name:      "list",
				Usage:     "olympia [--store PATH] player list --game CODE",
				ShortHelp: "list a game's players",
				Flags:     listFlags,
				Exec: func(ctx context.Context, args []string) error {
					if len(args) != 0 {
						return ErrUsage
					}
					if listGame == "" {
						return errors.New("--game is required")
					}
					code, err := gameCode(listGame)
					if err != nil {
						return err
					}
					return withStore(func(s *gmstore.Store) error {
						players, err := s.Players(ctx, code)
						if err != nil {
							return err
						}
						tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
						fmt.Fprintln(tw, "FACTION\tUPLOADER\tSTATUS\tNAME")
						for _, p := range players {
							fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Faction, p.Email, activeText(p.Active), p.Name)
						}
						return tw.Flush()
					})
				},
			},
			{
				Name:      "show",
				Usage:     "olympia [--store PATH] player show --game CODE --faction ID",
				ShortHelp: "show a player",
				Flags:     showFlags,
				Exec: func(ctx context.Context, args []string) error {
					code, f, err := showTarget(args)
					if err != nil {
						return err
					}
					return withStore(func(s *gmstore.Store) error {
						p, err := s.Player(ctx, code, f)
						if err != nil {
							return err
						}
						fmt.Fprintf(stdout, "game:     %s\nfaction:  %s\nuploader: %s\nname:     %s\nstatus:   %s\n", p.GameCode, p.Faction, p.Email, p.Name, activeText(p.Active))
						return nil
					})
				},
			},
			{
				Name:      "update",
				Usage:     "olympia [--store PATH] player update --game CODE --faction ID [--new-faction ID] [--uploader EMAIL] [--name NAME]",
				ShortHelp: "change a player's faction, uploader, or name",
				Flags:     updateFlags,
				Exec: func(ctx context.Context, args []string) error {
					code, f, err := updateTarget(args)
					if err != nil {
						return err
					}
					var change gmstore.PlayerChange
					if isSet(updateFlags, "new-faction") {
						nf, err := faction(newFaction)
						if err != nil {
							return err
						}
						change.Faction = &nf
					}
					if isSet(updateFlags, "uploader") {
						e, err := uploaderEmail(newEmail)
						if err != nil {
							return err
						}
						change.Email = &e
					}
					if isSet(updateFlags, "name") {
						n, err := playerName(newName)
						if err != nil {
							return err
						}
						change.Name = &n
					}
					if change == (gmstore.PlayerChange{}) {
						return ErrNoChange
					}
					return withStore(func(s *gmstore.Store) error {
						if err := s.UpdatePlayer(ctx, code, f, change); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "updated player %s in game %s\n", f, code)
						return nil
					})
				},
			},
			toggle("disable", false),
			toggle("enable", true),
			{
				Name:      "delete",
				Usage:     "olympia [--store PATH] player delete --game CODE --faction ID",
				ShortHelp: "delete a player that nothing refers to",
				Flags:     deleteFlags,
				Exec: func(ctx context.Context, args []string) error {
					code, f, err := deleteTarget(args)
					if err != nil {
						return err
					}
					return withStore(func(s *gmstore.Store) error {
						if err := s.DeletePlayer(ctx, code, f); err != nil {
							return err
						}
						fmt.Fprintf(stdout, "deleted player %s from game %s\n", f, code)
						return nil
					})
				},
			},
		},
	}
}

func stageCommand(rootFlags *ff.FlagSet, stdout io.Writer) *ff.Command {
	var server, email, spool, batch, game, turn string
	var rebuild, force bool
	stageFlags := ff.NewFlagSet("stage").SetParent(rootFlags)
	stageFlags.StringVar(&server, 0, "server", "", "fuingo's URL (OLYMPIA_SERVER)")
	stageFlags.StringVar(&email, 0, "email", "", "email of a fuingo account that manages the game (OLYMPIA_EMAIL)")
	stageFlags.StringVar(&spool, 0, "spool", "", "the engine's spool directory, <libdir>/spool (OLYMPIA_SPOOL)")
	stageFlags.StringVar(&batch, 0, "batch", "", "directory that holds the downloaded batches (OLYMPIA_BATCH)")
	stageFlags.StringVar(&game, 0, "game", "", "game code (command line only)")
	stageFlags.StringVar(&turn, 0, "turn", "", "turn number (command line only)")
	stageFlags.BoolVar(&rebuild, 0, "rebuild", "download the batch again even if it exists (command line only)")
	stageFlags.BoolVar(&force, 0, "force", "stage even if the spool already holds files starting with \"m\" (command line only)")
	return &ff.Command{
		Name:      "stage",
		Usage:     "olympia [--env ENV] stage [--server URL] [--email EMAIL] [--spool DIR] [--batch DIR] --game CODE --turn N [--rebuild] [--force]",
		ShortHelp: "download a turn's uploads from fuingo and write them to the engine's spool",
		LongHelp:  "OLYMPIA_PASSWORD must be set in the environment.",
		Flags:     stageFlags,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("%w %q", ErrUsage, args)
			}
			for name, v := range map[string]string{"--server": server, "--email": email, "--spool": spool, "--batch": batch, "--game": game, "--turn": turn} {
				if v == "" {
					return fmt.Errorf("%s is required", name)
				}
			}
			password := os.Getenv(EnvPrefix + "_PASSWORD")
			if password == "" {
				return ErrMissingPassword
			}
			client, err := olympia.NewClient(server, nil)
			if err != nil {
				return err
			}
			res, err := olympia.Stage(ctx, olympia.Options{
				Game:     credential.NormalizeGameCode(game),
				Turn:     turn,
				SpoolDir: spool,
				BatchDir: batch,
				Rebuild:  rebuild,
				Force:    force,
				Fetch: func(ctx context.Context) (olympia.Fetcher, error) {
					return client, client.Login(ctx, credential.NormalizeEmail(email), password)
				},
			})
			if err != nil {
				return err
			}
			how := "reused"
			if res.Downloaded {
				how = "downloaded"
			}
			fmt.Fprintf(stdout, "staged %d files for %s turn %s from the %s batch %s\n", len(res.Manifest.Files), res.Manifest.Game, res.Manifest.Turn, how, res.BatchDir)
			for _, f := range res.Manifest.Files {
				fmt.Fprintf(stdout, "  %s\n", f)
			}
			return nil
		},
	}
}
