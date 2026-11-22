// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cli holds the configuration steps shared by the commands.
package cli

import (
	"os"
	"strings"

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
//
// The flags named in commandLineOnly change what a command does rather than
// configure it, so they are honored only on the command line: Parse removes
// their environment variables, including any set by an environment file,
// before applying the environment.
func Parse(cmd *ff.Command, args []string, env *string, commandLineOnly ...string) error {
	if err := cmd.Parse(args); err != nil {
		return err
	}
	if *env == "" {
		*env = os.Getenv(EnvPrefix + "_ENV")
	}
	if err := dotenv.Load(*env); err != nil {
		return err
	}
	for _, name := range commandLineOnly {
		if err := os.Unsetenv(EnvVar(name)); err != nil {
			return err
		}
	}
	if err := cmd.Reset(); err != nil {
		return err
	}
	return cmd.Parse(args, ff.WithEnvVarPrefix(EnvPrefix))
}

// EnvVar returns the environment variable for the flag --name, as ff names it:
// --generate-password reads FUINGO_GENERATE_PASSWORD.
func EnvVar(name string) string {
	return EnvPrefix + "_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(name))
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
