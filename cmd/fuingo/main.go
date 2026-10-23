// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command fuingo is the operators' CLI for the application data. Its commands
// follow the pattern fuingo <noun> <verb>.
//
//	fuingo version
//	fuingo [--env ENV] [--db PATH] version --schema
//	fuingo [--env ENV] [--db PATH] game create CODE
//
// Every flag can also be set with a FUINGO_ environment variable:
//
//	--env   FUINGO_ENV   development, test, production, or agent (required by every
//	                     command but version without --schema)
//	--db    FUINGO_DB    path to the database file (required by commands that use it;
//	                     :memory: is refused)
//
// version --schema has no environment variable: version reads no
// environment unless --schema is on the command line.
//
// Commands that use the database only open one that is already at the schema
// version this program expects. fuingo never creates a database file or
// migrates one; fuingodb does.
//
// Game codes are lowercased with strings.ToLower, as uploads are, and must be
// a valid header word: one or more bytes in 0x21-0x7E. New games are active.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

const (
	ErrMissingDatabase = cerrs.Error("--db (FUINGO_DB) is required")
	ErrMemoryDatabase  = cerrs.Error("fuingo requires a database file, not :memory:")
	ErrGameCode        = cerrs.Error("game code must be one or more printable ASCII bytes with no spaces")
	ErrUsage           = cerrs.Error("wrong number of arguments")
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, ff.ErrHelp) {
			fmt.Fprintf(os.Stderr, "fuingo: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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
	game := &ff.Command{
		Name:        "game",
		Usage:       "fuingo [--db PATH] game <create>",
		ShortHelp:   "manage games",
		Flags:       ff.NewFlagSet("game").SetParent(rootFlags),
		Subcommands: []*ff.Command{gameCreate},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	root := &ff.Command{
		Name:        "fuingo",
		Usage:       "fuingo [--env ENV] [--db PATH] <version|game>",
		LongHelp:    "fuingo " + fuingo.Version().String(),
		Flags:       rootFlags,
		Subcommands: []*ff.Command{version, game},
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
	return cli.Parse(root, args, env)
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

// gameCode lowercases code and checks that it can appear as the first word of
// a turn file's header line.
func gameCode(code string) (string, error) {
	if code == "" {
		return "", ErrGameCode
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 0x21 || code[i] > 0x7e {
			return "", fmt.Errorf("%q: %w", code, ErrGameCode)
		}
	}
	return strings.ToLower(code), nil
}
