// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command fuingodb creates and migrates fuingo databases.
//
//	fuingodb [--env ENV] create --db PATH
//	fuingodb [--env ENV] migrate --db PATH
//
// Every flag can also be set with a FUINGO_ environment variable:
//
//	--env  FUINGO_ENV  development, test, production, or agent (required)
//	--db   FUINGO_DB   path to the database file (required; :memory: is refused)
//
// create also requires FUINGO_PEPPER. In development and agent, it seeds the
// new database with the default games, upload accounts, and API accounts in
// TESTING.md.
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
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

const (
	ErrMissingDatabase = cerrs.Error("--db (FUINGO_DB) is required")
	ErrMemoryDatabase  = cerrs.Error("fuingodb requires a database file, not :memory:")
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, ff.ErrHelp) {
			fmt.Fprintf(os.Stderr, "fuingodb: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var env, db string
	rootFlags := ff.NewFlagSet("fuingodb")
	rootFlags.StringVar(&env, 0, "env", "", "environment: development, test, production, or agent (FUINGO_ENV)")
	rootFlags.StringVar(&db, 0, "db", "", "path to the database file (FUINGO_DB)")

	create := &ff.Command{
		Name:      "create",
		Usage:     "fuingodb create --db PATH",
		ShortHelp: "create a new database; seeds default accounts in development and agent",
		Flags:     ff.NewFlagSet("create").SetParent(rootFlags),
		Exec: func(ctx context.Context, args []string) error {
			return createDatabase(ctx, stdout, env, db)
		},
	}
	migrate := &ff.Command{
		Name:      "migrate",
		Usage:     "fuingodb migrate --db PATH",
		ShortHelp: "apply pending migrations to an existing database",
		Flags:     ff.NewFlagSet("migrate").SetParent(rootFlags),
		Exec: func(ctx context.Context, args []string) error {
			return migrateDatabase(ctx, stdout, db)
		},
	}
	root := &ff.Command{
		Name:        "fuingodb",
		Usage:       "fuingodb [--env ENV] <create|migrate> --db PATH",
		LongHelp:    "fuingodb " + fuingo.Version().String(),
		Flags:       rootFlags,
		Subcommands: []*ff.Command{create, migrate},
		Exec: func(ctx context.Context, args []string) error {
			return ff.ErrHelp
		},
	}

	err := cli.Parse(root, args, &env)
	if err == nil {
		err = root.Run(ctx)
	}
	if errors.Is(err, ff.ErrHelp) {
		fmt.Fprint(stderr, ffhelp.Command(root.GetSelected()))
	}
	return err
}

func checkDatabasePath(db string) error {
	switch db {
	case "":
		return ErrMissingDatabase
	case ":memory:":
		return ErrMemoryDatabase
	}
	return nil
}

func createDatabase(ctx context.Context, stdout io.Writer, env, db string) (err error) {
	if err := checkDatabasePath(db); err != nil {
		return err
	}
	pepper, err := cli.Pepper()
	if err != nil {
		return err
	}
	s, err := store.Create(ctx, db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	if devdata.Allowed(env) {
		if err := devdata.Seed(ctx, s, env, pepper); err != nil {
			// The database was made by this call; do not leave it unseeded.
			err = errors.Join(err, s.Close())
			for _, name := range []string{db, db + "-wal", db + "-shm"} {
				_ = os.Remove(name)
			}
			return fmt.Errorf("seed %s: %w", db, err)
		}
		fmt.Fprintf(stdout, "created %s with the default %s accounts\n", db, env)
		return nil
	}
	fmt.Fprintf(stdout, "created %s\n", db)
	return nil
}

func migrateDatabase(ctx context.Context, stdout io.Writer, db string) error {
	if err := checkDatabasePath(db); err != nil {
		return err
	}
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "migrated %s\n", db)
	return nil
}
