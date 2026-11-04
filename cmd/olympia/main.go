// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command olympia is the game master's tool for Olympia G3 games whose
// players upload their orders to fuingo.
//
//	olympia version
//	olympia [--env ENV] stage [--server URL] [--email EMAIL] [--spool DIR] [--batch DIR]
//	        --game CODE --turn N [--rebuild] [--force]
//
// stage downloads every upload for a game's turn from fuingo's /api/ and
// writes each one, in upload order, to the engine's spool directory as an
// mbox-style message named m<turn>-<seq>-<email>-<upload id>. The engine
// reads each spool file, applies its orders, and deletes it.
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
// Every flag except --game, --turn, --rebuild, and --force can also be set
// with an OLYMPIA_ environment variable, read from the process environment
// or from the environment files that --env selects (see internal/dotenv).
// Explicit flags override the environment, which overrides the defaults:
//
//	--env     OLYMPIA_ENV     development, test, production, or agent (required by stage)
//	--server  OLYMPIA_SERVER  fuingo's URL, such as https://fuingo.example.com
//	--email   OLYMPIA_EMAIL   the email of a fuingo account that manages the game
//	--spool   OLYMPIA_SPOOL   the engine's spool directory, <libdir>/spool
//	--batch   OLYMPIA_BATCH   the directory that holds the batches
//
// OLYMPIA_PASSWORD is the account's API password. It has no flag: set it in
// the process environment or an ignored .local environment file. olympia
// never writes it, or the bearer token it logs in for, to any file.
//
// --game, --turn, --rebuild, and --force are honored only on the command
// line; their OLYMPIA_ variables are ignored.
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
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/olympia"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

// EnvPrefix prefixes olympia's environment variables.
const EnvPrefix = "OLYMPIA"

const (
	ErrMissingPassword = cerrs.Error("OLYMPIA_PASSWORD is missing or empty")
	ErrUsage           = cerrs.Error("unexpected arguments")
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, ff.ErrHelp) {
			fmt.Fprintf(os.Stderr, "olympia: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var env string
	rootFlags := ff.NewFlagSet("olympia")
	rootFlags.StringVar(&env, 0, "env", "", "environment: development, test, production, or agent (OLYMPIA_ENV)")

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
	stage := &ff.Command{
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

	root := &ff.Command{
		Name:        "olympia",
		Usage:       "olympia [--env ENV] <command> ...",
		LongHelp:    "olympia " + fuingo.Version().String(),
		Flags:       rootFlags,
		Subcommands: []*ff.Command{version, stage},
	}
	// version reads no environment files, so it needs no --env.
	if len(args) > 0 && args[0] == "version" {
		if err := root.Parse(args); err != nil {
			return helpOr(err, root, stderr)
		}
		return root.Run(ctx)
	}
	if err := cli.ParsePrefix(EnvPrefix, root, args, &env, "game", "turn", "rebuild", "force"); err != nil {
		return helpOr(err, root, stderr)
	}
	if root.GetSelected() == root {
		fmt.Fprint(stderr, ffhelp.Command(root))
		return ff.ErrHelp
	}
	return root.Run(ctx)
}

func helpOr(err error, root *ff.Command, stderr io.Writer) error {
	if errors.Is(err, ff.ErrHelp) {
		fmt.Fprint(stderr, ffhelp.Command(root.GetSelected()))
	}
	return err
}
