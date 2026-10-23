// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package devdata_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/store"
)

const pepper = "test-pepper"

func TestSeed(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"development", "agent"} {
		s, err := store.OpenPrivateMemory(t.Context(), t.Name()+env)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := devdata.Seed(t.Context(), s, env, pepper); err != nil {
			t.Fatalf("%s: Seed: %v", env, err)
		}
		games, err := s.Games(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(games, devdata.Games) {
			t.Errorf("%s: games = %v, want %v", env, games, devdata.Games)
		}
		for _, u := range devdata.Uploaders {
			if _, ok, err := s.FindUploader(t.Context(), credential.Hash(pepper, u.Email, u.Token)); err != nil || !ok {
				t.Errorf("%s: %s not found: %v", env, u.Email, err)
			}
			if _, ok, _ := s.FindUploader(t.Context(), credential.Hash("other", u.Email, u.Token)); ok {
				t.Errorf("%s: %s found under another pepper", env, u.Email)
			}
		}
		// Seeding never runs twice.
		if err := devdata.Seed(t.Context(), s, env, pepper); err == nil {
			t.Errorf("%s: second Seed succeeded", env)
		}
	}
}

func TestSeedRefusesOtherEnvironments(t *testing.T) {
	t.Parallel()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, env := range []string{"test", "production", "", "Development"} {
		if err := devdata.Seed(t.Context(), s, env, pepper); !errors.Is(err, devdata.ErrNotAllowed) {
			t.Errorf("Seed(%q) = %v, want ErrNotAllowed", env, err)
		}
	}
	if games, _ := s.Games(t.Context()); len(games) != 0 {
		t.Errorf("games = %v, want none", games)
	}
}

func TestSeedRequiresPepper(t *testing.T) {
	t.Parallel()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := devdata.Seed(t.Context(), s, "development", ""); !errors.Is(err, devdata.ErrMissingPepper) {
		t.Errorf("Seed = %v, want ErrMissingPepper", err)
	}
}
