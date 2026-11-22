// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package devdata_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestSeed(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"development", "agent"} {
		s, err := store.OpenPrivateMemory(t.Context(), t.Name()+env)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := devdata.Seed(t.Context(), s, env); err != nil {
			t.Fatalf("%s: Seed: %v", env, err)
		}
		if systems, err := s.Systems(t.Context()); err != nil || !slices.Equal(systems, []string{devdata.System}) {
			t.Errorf("%s: systems = %v, %v; want %s", env, systems, err, devdata.System)
		}
		games, err := s.Games(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var codes []string
		for _, g := range games {
			if g.System != devdata.System {
				t.Errorf("%s: game %s is not in %s", env, g, devdata.System)
			}
			codes = append(codes, g.Code)
		}
		if !slices.Equal(codes, devdata.Games) {
			t.Errorf("%s: games = %v, want %v", env, codes, devdata.Games)
		}
		for _, a := range devdata.Accounts {
			got, ok, err := s.FindAccount(t.Context(), a.Email)
			if err != nil || !ok {
				t.Errorf("%s: account %s not found: %v", env, a.Email, err)
				continue
			}
			if err := bcrypt.CompareHashAndPassword(got.HashedSecret, []byte(a.Password)); err != nil {
				t.Errorf("%s: account %s: password does not match: %v", env, a.Email, err)
			}
			if cost, err := bcrypt.Cost(got.HashedSecret); err != nil || cost != bcrypt.MinCost {
				t.Errorf("%s: account %s: bcrypt cost %d, %v; want MinCost", env, a.Email, cost, err)
			}
		}
		for _, code := range devdata.Games {
			game := store.GameRef{System: devdata.System, Code: code}
			var managers, players []string
			for _, a := range devdata.Accounts {
				if slices.Contains(a.Games, code) {
					managers = append(managers, a.Email)
				}
			}
			for _, p := range devdata.Players {
				if p.Game == code {
					players = append(players, p.Email)
				}
			}
			if got, err := s.Managers(t.Context(), game); err != nil || !slices.Equal(got, managers) {
				t.Errorf("%s: managers of %s = %v, %v; want %v", env, game, got, err, managers)
			}
			if got, err := s.Players(t.Context(), game); err != nil || !slices.Equal(got, players) {
				t.Errorf("%s: players of %s = %v, %v; want %v", env, game, got, err, players)
			}
		}
		// A default player has an account without a password.
		for _, p := range devdata.Players {
			if a, ok, err := s.FindAccount(t.Context(), p.Email); err != nil || !ok || a.HashedSecret != nil {
				t.Errorf("%s: player %s: account %+v, %v, %v; want one without a password", env, p.Email, a, ok, err)
			}
		}
		// Seeding never runs twice.
		if err := devdata.Seed(t.Context(), s, env); err == nil {
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
		if err := devdata.Seed(t.Context(), s, env); !errors.Is(err, devdata.ErrNotAllowed) {
			t.Errorf("Seed(%q) = %v, want ErrNotAllowed", env, err)
		}
	}
	if systems, _ := s.Systems(t.Context()); len(systems) != 0 {
		t.Errorf("systems = %v, want none", systems)
	}
}
