// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cli holds the configuration steps shared by the commands.
package cli

import (
	"os"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/dotenv"
	"github.com/peterbourgon/ff/v4"
)

const (
	ErrMissingPepper = cerrs.Error("FUINGO_PEPPER is missing or empty")
)

// EnvPrefix prefixes every environment variable: flag --db reads FUINGO_DB.
const EnvPrefix = "FUINGO"

// Parse parses args into cmd. Explicit flags override environment variables,
// which override defaults.
//
// The environment files depend on the environment, so Parse first parses the
// flags alone to find --env, falling back to FUINGO_ENV in the process
// environment. It loads that environment's files with internal/dotenv, then
// parses again with FUINGO_ environment variables. env must be the variable
// bound to the root command's --env flag.
func Parse(cmd *ff.Command, args []string, env *string) error {
	if err := cmd.Parse(args); err != nil {
		return err
	}
	if *env == "" {
		*env = os.Getenv(EnvPrefix + "_ENV")
	}
	if err := dotenv.Load(*env); err != nil {
		return err
	}
	if err := cmd.Reset(); err != nil {
		return err
	}
	return cmd.Parse(args, ff.WithEnvVarPrefix(EnvPrefix))
}

// Pepper returns FUINGO_PEPPER, which only ever comes from the environment
// (or an environment file loaded by Parse), never from a flag.
func Pepper() (string, error) {
	pepper := os.Getenv(EnvPrefix + "_PEPPER")
	if pepper == "" {
		return "", ErrMissingPepper
	}
	return pepper, nil
}
