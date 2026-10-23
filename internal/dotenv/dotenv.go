// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dotenv wraps the joho/godotenv package to load environment files.
package dotenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/joho/godotenv"
	"github.com/mdhender/fuingo/internal/cerrs"
)

const (
	ErrMissingEnvironment = cerrs.Error("missing environment")
	ErrUnknownEnvironment = cerrs.Error("unknown environment")
)

// Loader loads environment files from a filesystem into the process environment.
type Loader struct {
	fsys fs.FS
}

// New creates a loader that reads environment files from the root of fsys.
// The filesystem must not be nil.
func New(fsys fs.FS) *Loader {
	return &Loader{fsys: fsys}
}

// Load loads environment files from the working directory.
// See Loader.Load for supported environments and precedence.
func Load(env string) error {
	return New(os.DirFS(".")).Load(env)
}

// Load uses the `joho/godotenv` parser to load environment files from the loader's filesystem.
//
// The env must be one of "development", "test", "production", or "agent".
// The "agent" environment is reserved for the coding agent's local work.
//
// Load the following files depending on `env`, with the first file having the highest precedence,
// and .env having the lowest precedence:
//
// Priority  Filename__________   .gitignore?  Secrets?  Notes_______________________________
// Highest   .env.{{env}}.local   Yes          Yes       Environment-specific local overrides
// 2nd       .env.local           Yes          Yes       Local overrides
// 3rd       .env.{{env}}         No           Never     Shared environment-specific variables
// Lowest    .env                 No           Never     Shared for all environments
//
// Load never overwrites a variable that is already set, even if it is empty.
// The real process environment always wins over every file, and the first file
// in the list below that defines a key wins over the later ones.
//
// WARNING: we are incompatible with bkeepers/dotenv since we load `.env.local` in test.
// Read https://github.com/bkeepers/dotenv/issues/418 for the history of this decision.
func (l *Loader) Load(env string) error {
	if env == "" {
		return ErrMissingEnvironment
	}
	if env != "development" && env != "test" && env != "production" && env != "agent" {
		return ErrUnknownEnvironment
	}

	for _, path := range []string{
		".env." + env + ".local", // highest priority: .env.{{env}}.local
		".env.local",             // 2nd     priority: .env.local
		".env." + env,            // 3rd     priority: .env.{{env}}
		".env",                   // lowest  priority: .env
	} {
		// verify path exists and is a regular file
		sb, err := fs.Stat(l.fsys, path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !sb.Mode().IsRegular() {
			continue
		}
		data, err := fs.ReadFile(l.fsys, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		values, err := godotenv.UnmarshalBytes(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for key, value := range values {
			if _, exists := os.LookupEnv(key); !exists {
				_ = os.Setenv(key, value)
			}
		}
	}

	return nil
}
