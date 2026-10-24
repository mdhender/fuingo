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
	"golang.org/x/crypto/bcrypt"
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
			if _, ok, err := s.FindUploader(t.Context(), credential.Hash(pepper, u.Email, gameIDOf(t, s, u.Game), u.Token)); err != nil || !ok {
				t.Errorf("%s: %s not found: %v", env, u.Email, err)
			}
			if _, ok, _ := s.FindUploader(t.Context(), credential.Hash("other", u.Email, gameIDOf(t, s, u.Game), u.Token)); ok {
				t.Errorf("%s: %s found under another pepper", env, u.Email)
			}
		}
		for _, a := range devdata.Accounts {
			got, ok, err := s.FindAccount(t.Context(), a.Email)
			if err != nil || !ok {
				t.Errorf("%s: account %s not found: %v", env, a.Email, err)
				continue
			}
			if err := bcrypt.CompareHashAndPassword(got.PasswordHash, []byte(a.Password)); err != nil {
				t.Errorf("%s: account %s: password does not match: %v", env, a.Email, err)
			}
			if cost, err := bcrypt.Cost(got.PasswordHash); err != nil || cost != bcrypt.MinCost {
				t.Errorf("%s: account %s: bcrypt cost %d, %v; want MinCost", env, a.Email, cost, err)
			}
		}
		for _, game := range devdata.Games {
			var want []string
			for _, a := range devdata.Accounts {
				if slices.Contains(a.Games, game) {
					want = append(want, a.Email)
				}
			}
			if got, err := s.Managers(t.Context(), game); err != nil || !slices.Equal(got, want) {
				t.Errorf("%s: managers of %s = %v, %v; want %v", env, game, got, err, want)
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

// gameIDOf returns the ID of the game with the given code.
func gameIDOf(t *testing.T, s *store.Store, code string) int64 {
	t.Helper()
	id, ok, err := s.GameID(t.Context(), code)
	if err != nil || !ok {
		t.Fatalf("game %s: %v", code, err)
	}
	return id
}
