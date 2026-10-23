// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command fuingo is the operators' CLI.
//
//	fuingo version
//	fuingo [--env ENV] db --path PATH version
//
// Every flag can also be set with a FUINGO_ environment variable:
//
//	--env   FUINGO_ENV   development, test, production, or agent (required by db)
//	--path  FUINGO_PATH  path to the database file (required by db; :memory: is refused)
//
// The db commands only open a database that is already at the schema version
// this program expects; they never create or migrate one.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

const (
	ErrMissingDatabase = cerrs.Error("--path (FUINGO_PATH) is required")
	ErrMemoryDatabase  = cerrs.Error("fuingo requires a database file, not :memory:")
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

	version := &ff.Command{
		Name:      "version",
		Usage:     "fuingo version",
		ShortHelp: "print the program's version",
		Flags:     ff.NewFlagSet("version").SetParent(rootFlags),
		Exec: func(ctx context.Context, args []string) error {
			fmt.Fprintln(stdout, fuingo.Version().String())
			return nil
		},
	}

	dbFlags := ff.NewFlagSet("db").SetParent(rootFlags)
	dbFlags.StringVar(&path, 0, "path", "", "path to the database file (FUINGO_PATH)")
	dbVersion := &ff.Command{
		Name:      "version",
		Usage:     "fuingo db --path PATH version",
		ShortHelp: "print the database's schema version",
		Flags:     ff.NewFlagSet("db version").SetParent(dbFlags),
		Exec: func(ctx context.Context, args []string) error {
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
	db := &ff.Command{
		Name:        "db",
		Usage:       "fuingo db --path PATH <version>",
		ShortHelp:   "inspect a database",
		Flags:       dbFlags,
		Subcommands: []*ff.Command{dbVersion},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	root := &ff.Command{
		Name:        "fuingo",
		Usage:       "fuingo [--env ENV] <version|db>",
		LongHelp:    "fuingo " + fuingo.Version().String(),
		Flags:       rootFlags,
		Subcommands: []*ff.Command{version, db},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	err := parse(root, version, args, &env)
	if err == nil {
		err = root.Run(ctx)
	}
	if errors.Is(err, ff.ErrHelp) {
		fmt.Fprint(stderr, ffhelp.Command(root.GetSelected()))
	}
	return err
}

// parse parses args into root with cli.Parse, except that the version
// command needs no environment, so it is selected without loading one.
func parse(root, version *ff.Command, args []string, env *string) error {
	if err := root.Parse(args); err != nil || root.GetSelected() == version {
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
