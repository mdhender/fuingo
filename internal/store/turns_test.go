// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zombiezen.com/go/sqlite/sqlitex"
)

func TestTurns(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{})
	g1, _, _ := s.GameID(t.Context(), game01)
	g2, _, _ := s.GameID(t.Context(), game02)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	// The first turn is 0; nothing else is accepted.
	for _, n := range []int64{1, 5, -1} {
		if got, err := s.CreateTurn(t.Context(), g1, n, now); !errors.Is(err, ErrNotNextTurn) || got.Number != 0 {
			t.Errorf("CreateTurn(%d) in a new game = %+v, %v; want ErrNotNextTurn and next 0", n, got, err)
		}
	}
	for n := range int64(3) {
		got, err := s.CreateTurn(t.Context(), g1, n, now.Add(time.Duration(n)*time.Hour))
		if err != nil || got.Number != n || got.Closed() || !got.CreatedAt.Equal(now.Add(time.Duration(n)*time.Hour)) {
			t.Fatalf("CreateTurn(%d) = %+v, %v", n, got, err)
		}
	}
	// A repeat, a gap, and an earlier number are refused, naming the next.
	for _, n := range []int64{2, 4, 0} {
		if got, err := s.CreateTurn(t.Context(), g1, n, now); !errors.Is(err, ErrNotNextTurn) || got.Number != 3 {
			t.Errorf("CreateTurn(%d) = %+v, %v; want ErrNotNextTurn and next 3", n, got, err)
		}
	}
	// Each game numbers its own turns.
	if got, err := s.CreateTurn(t.Context(), g2, 0, now); err != nil || got.Number != 0 {
		t.Errorf("CreateTurn(0) in another game = %+v, %v", got, err)
	}
	turns, err := s.Turns(t.Context(), g1)
	if err != nil || len(turns) != 3 {
		t.Fatalf("Turns = %+v, %v; want three", turns, err)
	}
	for i, turn := range turns {
		if turn.Number != int64(i) || turn.Closed() {
			t.Errorf("turn %d = %+v", i, turn)
		}
	}
	if turns, _ := s.Turns(t.Context(), g2); len(turns) != 1 {
		t.Errorf("turns of game02 = %+v, want one", turns)
	}
}

func TestSetTurnClosed(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{})
	g1, _, _ := s.GameID(t.Context(), game01)
	g2, _, _ := s.GameID(t.Context(), game02)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for _, g := range []int64{g1, g2} {
		for n := range int64(2) {
			if _, err := s.CreateTurn(t.Context(), g, n, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	closedAt := now.Add(time.Hour)
	got, ok, err := s.SetTurnClosed(t.Context(), g1, 0, true, closedAt)
	if err != nil || !ok || got.Number != 0 || !got.ClosedAt.Equal(closedAt) {
		t.Fatalf("close = %+v, %v, %v", got, ok, err)
	}
	// Closing again keeps the first time.
	if got, ok, err := s.SetTurnClosed(t.Context(), g1, 0, true, closedAt.Add(time.Hour)); err != nil || !ok || !got.ClosedAt.Equal(closedAt) {
		t.Errorf("second close = %+v, %v, %v; want the first closing time", got, ok, err)
	}
	// Only that turn of that game closed.
	turns, _ := s.Turns(t.Context(), g1)
	if !turns[0].Closed() || turns[1].Closed() {
		t.Errorf("turns of game01 = %+v", turns)
	}
	for _, turn := range mustTurns(t, s, g2) {
		if turn.Closed() {
			t.Errorf("closing game01's turn closed game02's turn %d", turn.Number)
		}
	}
	// Turns may be created while earlier ones are closed or open.
	if _, err := s.CreateTurn(t.Context(), g1, 2, now); err != nil {
		t.Errorf("CreateTurn after a close: %v", err)
	}
	for range 2 { // reopening twice succeeds
		if got, ok, err := s.SetTurnClosed(t.Context(), g1, 0, false, now); err != nil || !ok || got.Closed() {
			t.Errorf("reopen = %+v, %v, %v", got, ok, err)
		}
	}
	// A reopened turn closes at the new time.
	later := closedAt.Add(48 * time.Hour)
	if got, _, _ := s.SetTurnClosed(t.Context(), g1, 0, true, later); !got.ClosedAt.Equal(later) {
		t.Errorf("close after reopening = %+v, want %v", got, later)
	}
	for _, closed := range []bool{true, false} {
		if _, ok, err := s.SetTurnClosed(t.Context(), g1, 9, closed, now); err != nil || ok {
			t.Errorf("SetTurnClosed of a missing turn = %v, %v; want false", ok, err)
		}
	}
}

func mustTurns(t *testing.T, s *Store, gameID int64) []Turn {
	t.Helper()
	turns, err := s.Turns(t.Context(), gameID)
	if err != nil {
		t.Fatal(err)
	}
	return turns
}

// Concurrent requests for the same next turn create it once.
func TestCreateTurnConcurrently(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{})
	g1, _, _ := s.GameID(t.Context(), game01)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for n := range int64(5) {
		var created atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				switch _, err := s.CreateTurn(t.Context(), g1, n, now); {
				case err == nil:
					created.Add(1)
				case !errors.Is(err, ErrNotNextTurn):
					t.Errorf("CreateTurn(%d): %v", n, err)
				}
			})
		}
		wg.Wait()
		if created.Load() != 1 {
			t.Errorf("turn %d was created %d times, want 1", n, created.Load())
		}
	}
	if turns := mustTurns(t, s, g1); len(turns) != 5 {
		t.Errorf("%d turns, want 5", len(turns))
	}
}

func TestTurnsBlockDeletingTheirGame(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{})
	g1, _, _ := s.GameID(t.Context(), game01)
	if _, err := s.CreateTurn(t.Context(), g1, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	if err := sqlitex.ExecuteTransient(conn, "DELETE FROM games WHERE id = 1;", nil); !isForeignKeyError(err) {
		t.Errorf("deleting a game with turns = %v, want a foreign key error", err)
	}
}
