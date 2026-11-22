// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"errors"
	"testing"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// uploadFixture is a store with turn 0 in both default games and an
// account to upload as.
func uploadFixture(t *testing.T) (s *Store, g1, g2, account int64) {
	t.Helper()
	s = openMemory(t)
	seed(t, s, Rows{})
	g1, _, _ = s.GameID(t.Context(), game01)
	g2, _, _ = s.GameID(t.Context(), game02)
	for _, g := range []int64{g1, g2} {
		if _, err := s.CreateTurn(t.Context(), g, 0, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	account, err := s.CreateAccount(t.Context(), NewAccount{Email: "gm@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return s, g1, g2, account
}

func count(t *testing.T, s *Store, query string) int {
	t.Helper()
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	n, err := sqlitex.ResultInt(conn.Prep(query))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Identical contents are stored once, across games, and never rewritten.
func TestUploadContentsStoredOnce(t *testing.T) {
	t.Parallel()
	s, g1, g2, account := uploadFixture(t)
	first := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	contents := []byte("\xef\xbb\xbforders\r\n")
	var ids []int64
	for i, g := range []int64{g1, g2, g1} {
		u, err := s.CreateUpload(t.Context(), NewUpload{
			GameID: g, Folder: Folder{Turn: new(int64(0))}, Name: "orders.txt", Contents: contents,
			AccountID: account, Email: "gm@example.com", Now: first.Add(time.Duration(i) * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	if n := count(t, s, "SELECT count(*) FROM file_contents;"); n != 1 {
		t.Errorf("%d rows of contents, want 1", n)
	}
	if n := count(t, s, "SELECT count(*) FROM uploads;"); n != 3 {
		t.Errorf("%d uploads, want 3", n)
	}
	// The contents row is the one the first upload wrote.
	if n := count(t, s, "SELECT count(*) FROM file_contents WHERE created_at = '"+first.Format(timeLayout)+"';"); n != 1 {
		t.Error("the stored contents were rewritten by a later upload")
	}
	u, got, found, err := s.GameUploadContents(t.Context(), g2, ids[1])
	if err != nil || !found || string(got) != string(contents) || u.Size != int64(len(contents)) {
		t.Errorf("contents = %q, %v, %v", got, found, err)
	}
	// An upload is found only in its own game.
	if _, _, found, err := s.GameUploadContents(t.Context(), g2, ids[0]); err != nil || found {
		t.Errorf("game01's upload through game02: found %v, %v", found, err)
	}
	if _, found, err := s.GameUpload(t.Context(), g1, ids[1]); err != nil || found {
		t.Errorf("game02's upload through game01: found %v, %v", found, err)
	}
}

// A refused upload stores neither a record nor contents.
func TestUploadToMissingFolderStoresNothing(t *testing.T) {
	t.Parallel()
	s, g1, _, account := uploadFixture(t)
	for name, tc := range map[string]struct {
		folder Folder
		want   error
	}{
		"no such turn":        {Folder{Turn: new(int64(7))}, ErrUnknownTurn},
		"no such player":      {Folder{Turn: new(int64(0)), Player: new(int64(999))}, ErrUnknownPlayer},
		"player without turn": {Folder{Player: new(int64(1))}, ErrUnknownTurn},
	} {
		_, err := s.CreateUpload(t.Context(), NewUpload{
			GameID: g1, Folder: tc.folder, Name: "x.txt", Contents: []byte("x"),
			AccountID: account, Email: "gm@example.com", Now: time.Now(),
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
		if _, err := s.FolderUploads(t.Context(), g1, tc.folder, true); !errors.Is(err, tc.want) {
			t.Errorf("list %s: %v, want %v", name, err, tc.want)
		}
	}
	if n := count(t, s, "SELECT (SELECT count(*) FROM uploads) + (SELECT count(*) FROM file_contents);"); n != 0 {
		t.Errorf("%d rows stored by refused uploads", n)
	}
}

// Withdrawing keeps the row and the contents, and rows cannot be removed
// from under an upload.
func TestWithdrawKeepsEverything(t *testing.T) {
	t.Parallel()
	s, g1, _, account := uploadFixture(t)
	u, err := s.CreateUpload(t.Context(), NewUpload{
		GameID: g1, Name: "rules.txt", Contents: []byte("rules\n"),
		AccountID: account, Email: "gm@example.com", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	if got, err := s.WithdrawUpload(t.Context(), g1, u.ID, account, at); err != nil || !got.WithdrawnAt.Equal(at) {
		t.Fatalf("withdraw = %+v, %v", got, err)
	}
	if got, err := s.WithdrawUpload(t.Context(), g1, u.ID, account, at.Add(time.Hour)); err != nil || !got.WithdrawnAt.Equal(at) {
		t.Errorf("second withdraw = %+v, %v; want the first time", got, err)
	}
	if _, err := s.WithdrawUpload(t.Context(), g1, u.ID, account+1000, at); !errors.Is(err, ErrNotUploader) {
		t.Errorf("withdraw by another account: %v", err)
	}
	if _, err := s.WithdrawUpload(t.Context(), g1, u.ID+1000, account, at); !errors.Is(err, ErrUnknownUpload) {
		t.Errorf("withdraw of a missing upload: %v", err)
	}
	if n := count(t, s, "SELECT (SELECT count(*) FROM uploads) + (SELECT count(*) FROM file_contents);"); n != 2 {
		t.Errorf("%d rows after withdrawing, want the upload and its contents", n)
	}
	if got, _ := s.FolderUploads(t.Context(), g1, Folder{}, false); len(got) != 0 {
		t.Errorf("listed after withdrawal: %+v", got)
	}
	if got, _ := s.FolderUploads(t.Context(), g1, Folder{}, true); len(got) != 1 || !got[0].Withdrawn() {
		t.Errorf("record after withdrawal: %+v", got)
	}
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	for _, q := range []string{"DELETE FROM file_contents;", "DELETE FROM games;", "DELETE FROM accounts WHERE email = 'gm@example.com';"} {
		if err := sqlitex.ExecuteTransient(conn, q, nil); sqlite.ErrCode(err).ToPrimary() != sqlite.ResultConstraint {
			t.Errorf("%s = %v, want a constraint error", q, err)
		}
	}
}

// An account's access to a game follows from its active rows there.
func TestGameAccess(t *testing.T) {
	t.Parallel()
	s := openMemory(t)
	seed(t, s, Rows{
		Accounts: []NewAccount{{Email: "acct01@example.com"}},
		Players:  []Member{{Game: game01, Email: "catbird@example.com"}},
		Managers: []Member{{Game: game01, Email: "acct01@example.com"}},
	})
	ctx := t.Context()
	gm, _, _ := s.FindAccount(ctx, "acct01@example.com")
	cat, _, _ := s.FindAccount(ctx, "catbird@example.com")
	// The GM of game01 also plays in game02.
	added, err := s.AddPlayer(ctx, Member{Game: game02, Email: gm.Email})
	if err != nil {
		t.Fatal(err)
	}
	games, err := s.AccountGames(ctx, gm.ID)
	if err != nil || len(games) != 2 || games[0].Game != game01 || !games[0].Manager || games[0].Player != 0 ||
		games[1].Game != game02 || games[1].Manager || games[1].Player != added.ID {
		t.Fatalf("AccountGames = %+v, %v", games, err)
	}
	if a, ok, err := s.GameAccess(ctx, cat.ID, game01); err != nil || !ok || a.Manager || a.Player == 0 || !a.Active {
		t.Errorf("a player's access = %+v, %v, %v", a, ok, err)
	}
	for _, g := range []GameRef{game02, {System: "system01", Code: "nope"}, {System: "nope", Code: "game01"}} {
		if _, ok, err := s.GameAccess(ctx, cat.ID, g); err != nil || ok {
			t.Errorf("a player's access to %s: %v, %v", g, ok, err)
		}
	}
	// An inactive game is closed to its players and open to its GM.
	if err := s.SetGameActive(ctx, game01, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GameAccess(ctx, cat.ID, game01); ok {
		t.Error("a player reaches an inactive game")
	}
	if a, ok, _ := s.GameAccess(ctx, gm.ID, game01); !ok || a.Active || !a.Manager {
		t.Errorf("a GM's access to an inactive game = %+v, %v", a, ok)
	}
	if games, _ := s.AccountGames(ctx, cat.ID); len(games) != 0 {
		t.Errorf("a player's games = %+v", games)
	}
	// An inactive manager has no access, and an inactive account none at all.
	if err := s.SetManagerActive(ctx, Member{Game: game01, Email: gm.Email}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GameAccess(ctx, gm.ID, game01); ok {
		t.Error("an inactive manager reaches the game")
	}
	if err := s.SetAccountActive(ctx, gm.Email, false); err != nil {
		t.Fatal(err)
	}
	if games, _ := s.AccountGames(ctx, gm.ID); len(games) != 0 {
		t.Errorf("an inactive account's games = %+v", games)
	}
}

// A player's upload and withdrawal are refused once the turn is closed,
// inside the transaction that would make them.
func TestClosedTurnRefusesPlayers(t *testing.T) {
	t.Parallel()
	s, g1, _, gm := uploadFixture(t)
	ctx := t.Context()
	if _, err := s.AddPlayer(ctx, Member{Game: game01, Email: "catbird@example.com"}); err != nil {
		t.Fatal(err)
	}
	cat, _, _ := s.FindAccount(ctx, "catbird@example.com")
	access, _, _ := s.GameAccess(ctx, cat.ID, game01)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	folder := Folder{Turn: new(int64(0)), Player: &access.Player}
	upload := func(account int64, name string, open bool) (Upload, error) {
		return s.CreateUpload(ctx, NewUpload{GameID: g1, Folder: folder, Name: name, Contents: []byte(name), AccountID: account, Email: "x@example.com", Now: now, OpenTurnOnly: open})
	}
	mine, err := upload(cat.ID, "orders.txt", true)
	if err != nil || mine.UploaderID != cat.ID {
		t.Fatalf("upload to an open turn: %+v, %v", mine, err)
	}
	gms, _ := upload(gm, "report.txt", false)
	// A player withdraws only their own file from their own folder.
	if _, err := s.WithdrawPlayerUpload(ctx, g1, gms.ID, cat.ID, access.Player, now); !errors.Is(err, ErrNotUploader) {
		t.Errorf("withdraw the GM's file: %v", err)
	}
	if _, err := s.WithdrawPlayerUpload(ctx, g1, mine.ID, cat.ID, access.Player+1, now); !errors.Is(err, ErrUnknownUpload) {
		t.Errorf("withdraw from another folder: %v", err)
	}
	if _, _, err := s.SetTurnClosed(ctx, g1, 0, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := upload(cat.ID, "late.txt", true); !errors.Is(err, ErrTurnClosed) {
		t.Errorf("upload to a closed turn: %v", err)
	}
	if _, err := s.WithdrawPlayerUpload(ctx, g1, mine.ID, cat.ID, access.Player, now); !errors.Is(err, ErrTurnClosed) {
		t.Errorf("withdraw from a closed turn: %v", err)
	}
	if _, err := upload(gm, "late-report.txt", false); err != nil {
		t.Errorf("a GM's upload to a closed turn: %v", err)
	}
	if n := count(t, s, "SELECT count(*) FROM uploads WHERE withdrawn_at IS NOT NULL OR name = 'late.txt'"); n != 0 {
		t.Errorf("%d refused changes were made", n)
	}
	if _, _, err := s.SetTurnClosed(ctx, g1, 0, false, now); err != nil {
		t.Fatal(err)
	}
	u, err := s.WithdrawPlayerUpload(ctx, g1, mine.ID, cat.ID, access.Player, now.Add(time.Minute))
	if err != nil || !u.WithdrawnAt.Equal(now.Add(time.Minute)) {
		t.Errorf("withdraw from a reopened turn: %+v, %v", u, err)
	}
	// Withdrawn, it is no longer the player's to withdraw.
	if _, err := s.WithdrawPlayerUpload(ctx, g1, mine.ID, cat.ID, access.Player, now); !errors.Is(err, ErrUnknownUpload) {
		t.Errorf("withdraw a withdrawn file: %v", err)
	}
	counts, err := s.TurnFolderCounts(ctx, g1, 0)
	if err != nil || len(counts) != 1 || counts[access.Player] != 2 {
		t.Errorf("TurnFolderCounts = %v, %v", counts, err)
	}
	if turn, found, err := s.FindTurn(ctx, g1, 0); err != nil || !found || turn.Closed() {
		t.Errorf("FindTurn = %+v, %v, %v", turn, found, err)
	}
	if _, found, _ := s.FindTurn(ctx, g1, 5); found {
		t.Error("FindTurn found a turn the game does not have")
	}
}

// System-level files belong to their system alone: the administrator adds
// and withdraws them, no game's functions return them, and a system's
// functions return no game's files.
func TestSystemUploads(t *testing.T) {
	t.Parallel()
	s, g1, _, account := uploadFixture(t)
	if _, err := s.CreateSystem(t.Context(), "other"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	rules := []byte("\xef\xbb\xbfrules\r\n")
	inGame, err := s.CreateUpload(t.Context(), NewUpload{GameID: g1, Name: "rules.txt", Contents: rules, AccountID: account, Email: "gm@example.com", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSystemUpload(t.Context(), "nope", "rules.txt", rules, now); !errors.Is(err, ErrUnknownSystem) {
		t.Errorf("unknown system: %v", err)
	}
	if _, err := s.SystemUploads(t.Context(), "nope", true); !errors.Is(err, ErrUnknownSystem) {
		t.Errorf("list unknown system: %v", err)
	}
	first, err := s.CreateSystemUpload(t.Context(), "system01", "rules.txt", rules, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateSystemUpload(t.Context(), "system01", "rules.txt", []byte("new rules\n"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.UploaderID != 0 || first.UploaderEmail != "" || first.Turn != nil || first.Player != nil || first.Size != int64(len(rules)) || first.SHA256 != inGame.SHA256 {
		t.Errorf("system upload = %+v", first)
	}
	// The same bytes are stored once, at whatever level.
	if n := count(t, s, "SELECT count(*) FROM file_contents;"); n != 2 {
		t.Errorf("file_contents has %d rows, want 2", n)
	}
	ids := func(us []Upload) (out []int64) {
		for _, u := range us {
			out = append(out, u.ID)
		}
		return out
	}
	list, err := s.SystemUploads(t.Context(), "system01", false)
	if err != nil || len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID || !list[0].UploadedAt.Equal(now) {
		t.Fatalf("SystemUploads = %v, %v", ids(list), err)
	}
	if list, err := s.SystemUploads(t.Context(), "other", true); err != nil || len(list) != 0 {
		t.Errorf("other system: %v, %v", ids(list), err)
	}
	// No game reaches a system's file, and no system a game's.
	if all, _ := s.GameUploads(t.Context(), g1, 0, true); len(all) != 1 || all[0].ID != inGame.ID {
		t.Errorf("GameUploads = %v", ids(all))
	}
	if list, _ := s.FolderUploads(t.Context(), g1, Folder{}, true); len(list) != 1 {
		t.Errorf("FolderUploads = %v", ids(list))
	}
	if _, found, _ := s.GameUpload(t.Context(), g1, first.ID); found {
		t.Error("a game reached a system-level file")
	}
	if _, _, found, _ := s.GameUploadContents(t.Context(), g1, first.ID); found {
		t.Error("a game reached a system-level file's contents")
	}
	if _, err := s.WithdrawUpload(t.Context(), g1, first.ID, account, now); !errors.Is(err, ErrUnknownUpload) {
		t.Errorf("withdraw through a game: %v", err)
	}
	for _, c := range []struct {
		system string
		id     int64
	}{{"system01", inGame.ID}, {"other", first.ID}, {"nope", first.ID}, {"system01", 999}} {
		if _, _, found, err := s.SystemUploadContents(t.Context(), c.system, c.id, true); found || err != nil {
			t.Errorf("SystemUploadContents(%s, %d) found = %v, %v", c.system, c.id, found, err)
		}
		if _, err := s.WithdrawSystemUpload(t.Context(), c.system, c.id, now); !errors.Is(err, ErrUnknownUpload) {
			t.Errorf("WithdrawSystemUpload(%s, %d) = %v", c.system, c.id, err)
		}
	}
	if _, got, found, err := s.SystemUploadContents(t.Context(), "system01", first.ID, false); !found || err != nil || string(got) != string(rules) {
		t.Errorf("contents = %q, %v, %v", got, found, err)
	}

	// Withdrawing keeps the record and the first time, and deletes nothing.
	at := now.Add(2 * time.Hour)
	if u, err := s.WithdrawSystemUpload(t.Context(), "system01", first.ID, at); err != nil || !u.WithdrawnAt.Equal(at) {
		t.Fatalf("withdraw = %+v, %v", u, err)
	}
	if u, err := s.WithdrawSystemUpload(t.Context(), "system01", first.ID, at.Add(time.Hour)); err != nil || !u.WithdrawnAt.Equal(at) {
		t.Errorf("second withdraw = %+v, %v", u, err)
	}
	if list, _ := s.SystemUploads(t.Context(), "system01", false); len(list) != 1 || list[0].ID != second.ID {
		t.Errorf("after withdrawing: %v", ids(list))
	}
	if list, _ := s.SystemUploads(t.Context(), "system01", true); len(list) != 2 || !list[0].Withdrawn() {
		t.Errorf("with withdrawn: %v", ids(list))
	}
	if _, _, found, _ := s.SystemUploadContents(t.Context(), "system01", first.ID, false); found {
		t.Error("a withdrawn file was given out")
	}
	if _, got, found, _ := s.SystemUploadContents(t.Context(), "system01", first.ID, true); !found || string(got) != string(rules) {
		t.Error("a withdrawn file's contents are gone")
	}
	if n := count(t, s, "SELECT count(*) FROM uploads;"); n != 3 {
		t.Errorf("uploads has %d rows, want 3", n)
	}
}

// The schema ties an upload's level to its uploader: a game's upload has an
// account, and a system's has none.
func TestUploadUploaderMatchesLevel(t *testing.T) {
	t.Parallel()
	s, _, _, _ := uploadFixture(t)
	if _, err := s.CreateSystemUpload(t.Context(), "system01", "a.txt", []byte("a"), time.Now()); err != nil {
		t.Fatal(err)
	}
	conn, err := s.take(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Put(conn)
	for _, q := range []string{
		"INSERT INTO uploads (system_id, name, sha256, uploader_account_id, uploader_email, uploaded_at) SELECT 1, 'a.txt', sha256, 1, 'gm@example.com', 'x' FROM file_contents;",
		"INSERT INTO uploads (system_id, name, sha256, uploader_email, uploaded_at) SELECT 1, 'a.txt', sha256, 'gm@example.com', 'x' FROM file_contents;",
		"INSERT INTO uploads (system_id, game_id, name, sha256, uploader_email, uploaded_at) SELECT 1, 1, 'a.txt', sha256, '', 'x' FROM file_contents;",
		"INSERT INTO uploads (system_id, game_id, name, sha256, uploader_account_id, uploader_email, uploaded_at) SELECT 1, 1, 'a.txt', sha256, 1, '', 'x' FROM file_contents;",
	} {
		if err := sqlitex.ExecuteTransient(conn, q, nil); sqlite.ErrCode(err) != sqlite.ResultConstraintCheck {
			t.Errorf("%s\n= %v, want a CHECK failure", q, err)
		}
	}
}
