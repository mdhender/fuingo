// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/store"
)

const bom = "\xef\xbb\xbf"

// upload returns a POST of a file to the folder at path.
func upload(path, token, name, contents string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path+"/files?name="+name, strings.NewReader(contents))
	r.Header.Set("Content-Type", "text/plain")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func sha(contents string) string {
	sum := sha256.Sum256([]byte(contents))
	return hex.EncodeToString(sum[:])
}

// filesFixture is a fixture with turns 0 and 1 in game01 and turn 0 in
// game02, and tokens for both GMs.
type filesFixture struct {
	*fixture
	gm1, gm2 string
	// IDs of the default players.
	catbird, spoonbill, caracara int64
}

func newFilesFixture(t *testing.T, maxUpload int64) *filesFixture {
	t.Helper()
	f := &filesFixture{fixture: newFixtureWithLimit(t, maxUpload)}
	f.gm1 = f.login(t, acct01.Email, acct01.Password)
	f.gm2 = f.login(t, acct02.Email, acct02.Password)
	decode[api.TurnStatus](t, f.fixture, post(game01Turns, f.gm1, `{"turn":0}`), http.StatusCreated)
	decode[api.TurnStatus](t, f.fixture, post(game01Turns, f.gm1, `{"turn":1}`), http.StatusCreated)
	decode[api.TurnStatus](t, f.fixture, post(game02Turns, f.gm2, `{"turn":0}`), http.StatusCreated)
	for _, p := range decode[api.PlayerList](t, f.fixture, get(game01Players, f.gm1), http.StatusOK).Players {
		switch p.Email {
		case catbird.Email:
			f.catbird = p.ID
		case spoonbill.Email:
			f.spoonbill = p.ID
		}
	}
	f.caracara = decode[api.PlayerList](t, f.fixture, get(game02Players, f.gm2), http.StatusOK).Players[0].ID
	if f.catbird == 0 || f.spoonbill == 0 || f.caracara == 0 {
		t.Fatal("default players not found")
	}
	return f
}

// folder returns the path of a player's folder in a turn of game01.
func playerFolder(turn, player int64) string {
	return fmt.Sprintf("%s/turns/%d/players/%d", game01Path, turn, player)
}

func (f *filesFixture) put(t *testing.T, path, token, name, contents string) api.File {
	t.Helper()
	return decode[api.File](t, f.fixture, upload(path, token, name, contents), http.StatusCreated)
}

// names lists the folder's files as "id:name".
func (f *filesFixture) list(t *testing.T, path, token string) []string {
	t.Helper()
	var got []string
	for _, file := range decode[api.FileList](t, f.fixture, get(path+"/files", token), http.StatusOK).Files {
		got = append(got, fmt.Sprintf("%d:%s", file.ID, file.Name))
	}
	return got
}

// uploads lists the game's uploads matching the query as their IDs.
func (f *filesFixture) uploadIDs(t *testing.T, game, token, query string) []int64 {
	t.Helper()
	ids := []int64{}
	for _, file := range decode[api.UploadList](t, f.fixture, get(game+"/uploads"+query, token), http.StatusOK).Uploads {
		ids = append(ids, file.ID)
	}
	return ids
}

func TestUploadAtEachLevel(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	at := f.clock.Now().Format(api.TimeLayout)
	turn1 := game01Path + "/turns/1"
	folder := playerFolder(1, f.catbird)

	// A BOM, CRLF line endings, a tab, and non-ASCII text are stored as sent.
	report := bom + "Turn 1 report\r\n\tnaïve café 命令\r\n"
	rules := "<html><script>alert(1)</script></html>\n"
	g := f.put(t, game01Path, f.gm1, "rules.html", rules)
	tn := f.put(t, turn1, f.gm1, "Turn-1_news.txt", "news\n")
	p := f.put(t, folder, f.gm1, "report.txt", report)

	if g.ID >= tn.ID || tn.ID >= p.ID {
		t.Errorf("IDs %d, %d, %d do not increase", g.ID, tn.ID, p.ID)
	}
	if g.Turn != nil || g.Player != nil || tn.Turn == nil || *tn.Turn != 1 || tn.Player != nil {
		t.Errorf("levels: game %+v, turn %+v", g, tn)
	}
	if p.System != "system01" || p.Game != "game01" || p.Turn == nil || *p.Turn != 1 || p.Player == nil || *p.Player != f.catbird ||
		p.Name != "report.txt" || p.Size != int64(len(report)) || p.SHA256 != sha(report) ||
		p.UploadedBy != acct01.Email || p.UploadedAt != at || p.WithdrawnAt != nil {
		t.Errorf("upload = %+v", p)
	}

	// Each folder lists only its own files, not those under or over it.
	for path, want := range map[string]string{
		game01Path:                   fmt.Sprintf("%d:rules.html", g.ID),
		turn1:                        fmt.Sprintf("%d:Turn-1_news.txt", tn.ID),
		folder:                       fmt.Sprintf("%d:report.txt", p.ID),
		game01Path + "/turns/0":      "",
		playerFolder(0, f.catbird):   "",
		playerFolder(1, f.spoonbill): "",
		game02Path:                   "",
		game02Path + "/turns/0":      "",
	} {
		token := f.gm1
		if strings.HasPrefix(path, game02Path) {
			token = f.gm2
		}
		if got := strings.Join(f.list(t, path, token), ","); got != want {
			t.Errorf("files in %s = %q, want %q", path, got, want)
		}
	}
	// An empty folder lists [], not null, and names itself.
	w := f.serve(get(playerFolder(0, f.catbird)+"/files", f.gm1))
	if body := w.Body.String(); !strings.Contains(body, `"files":[]`) || !strings.Contains(body, `"turn":0`) || !strings.Contains(body, fmt.Sprintf(`"player":%d`, f.catbird)) {
		t.Errorf("empty folder: %s", body)
	}
	if body := f.serve(get(game01Path+"/files", f.gm1)).Body.String(); !strings.Contains(body, `"turn":null,"player":null`) {
		t.Errorf("game folder: %s", body)
	}

	// The record, and the contents byte for byte.
	if got := decode[api.File](t, f.fixture, get(fmt.Sprintf("%s/uploads/%d", game01Path, p.ID), f.gm1), http.StatusOK); got.SHA256 != sha(report) || got.Name != "report.txt" || got.WithdrawnAt != nil {
		t.Errorf("record = %+v", got)
	}
	w = f.serve(get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, p.ID), f.gm1))
	if w.Code != http.StatusOK || w.Body.String() != report {
		t.Fatalf("download: %d %q, want the bytes uploaded", w.Code, w.Body)
	}
	for header, want := range map[string]string{
		"X-Content-SHA256":        sha(report),
		"Content-Type":            "application/octet-stream",
		"Content-Disposition":     `attachment; filename="report.txt"`,
		"Content-Length":          strconv.Itoa(len(report)),
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "sandbox; default-src 'none'",
		"Cache-Control":           "no-store",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("download %s = %q, want %q", header, got, want)
		}
	}
	// HTML is served as a download too, never as a page.
	w = f.serve(get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, g.ID), f.gm1))
	if w.Body.String() != rules || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("HTML download: %q %v", w.Body, w.Header())
	}
	// No contents reach the log.
	if logs := f.logs.String(); strings.Contains(logs, "Turn 1 report") || strings.Contains(logs, "alert(1)") {
		t.Error("file contents were logged")
	} else if !strings.Contains(logs, "uploaded file") {
		t.Error("the upload was not logged")
	}
}

// Every upload is kept: a second file with the same name replaces nothing.
func TestUploadReplacesNothing(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	folder := playerFolder(0, f.catbird)
	first := f.put(t, folder, f.gm1, "orders.txt", "first\n")
	f.clock.Advance(time.Minute)
	second := f.put(t, folder, f.gm1, "orders.txt", "second\n")
	third := f.put(t, folder, f.gm1, "orders.txt", "first\n") // the same bytes again
	want := fmt.Sprintf("%d:orders.txt,%d:orders.txt,%d:orders.txt", first.ID, second.ID, third.ID)
	if got := strings.Join(f.list(t, folder, f.gm1), ","); got != want {
		t.Errorf("files = %q, want %q", got, want)
	}
	if first.UploadedAt == second.UploadedAt || first.SHA256 != third.SHA256 || first.SHA256 == second.SHA256 {
		t.Errorf("records: %+v %+v %+v", first, second, third)
	}
	for id, want := range map[int64]string{first.ID: "first\n", second.ID: "second\n", third.ID: "first\n"} {
		if w := f.serve(get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, id), f.gm1)); w.Body.String() != want {
			t.Errorf("upload %d = %q, want %q", id, w.Body, want)
		}
	}
}

func TestUploadRefusals(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	folder := playerFolder(0, f.catbird)
	check := func(name string, r *http.Request, status int) {
		t.Helper()
		w := f.serve(r)
		if w.Code != status {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, status)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type %q", name, ct)
		}
	}
	// Names. Each is sent percent-encoded, so the server sees the character.
	for _, name := range []string{
		"", strings.Repeat("a", 49), ".hidden", "trailing.",
		"my%20orders.txt", "a+b", "a%2Fb", "a%5Cb", "a%3Cb", "a%3Eb", "a%26b", "a%3Bb", "a%22b", "a%27b", "a%60b",
		"a%00b", "a%09b", "%C3%B3rdenes.txt",
	} {
		check("name "+name, upload(folder, f.gm1, name, "orders\n"), http.StatusBadRequest)
	}
	r := upload(folder, f.gm1, "x", "orders\n")
	r.URL.RawQuery = ""
	check("no name", r, http.StatusBadRequest)

	// Contents.
	for name, contents := range map[string]string{
		"empty":         "",
		"only a BOM":    bom,
		"NUL":           "a\x00b\n",
		"escape":        "a\x1bb\n",
		"bad start":     "\xffabc\n",
		"bad middle":    "ab\xffcd\n",
		"bad end":       "abcd\xc3",
		"NUL after BOM": bom + "\x00",
	} {
		check(name, upload(folder, f.gm1, "orders.txt", contents), http.StatusBadRequest)
	}

	// The body must be the file itself.
	for _, ct := range []string{"", "application/json", "multipart/form-data; boundary=x", "application/x-www-form-urlencoded", "text/html"} {
		r := upload(folder, f.gm1, "orders.txt", "orders\n")
		r.Header.Del("Content-Type")
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		check("type "+ct, r, http.StatusUnsupportedMediaType)
	}

	// Folders that do not exist, in the caller's game and outside it.
	for name, path := range map[string]string{
		"no such turn":           game01Path + "/turns/2",
		"player in no turn":      playerFolder(2, f.catbird),
		"no such player":         playerFolder(0, 999),
		"another game's player":  playerFolder(0, f.caracara),
		"player 0":               playerFolder(0, 0),
		"turn 00":                game01Path + "/turns/00",
		"turn -1":                game01Path + "/turns/-1",
		"player abc":             game01Path + "/turns/0/players/abc",
		"another GM's game":      game02Path,
		"another GM's turn":      game02Path + "/turns/0",
		"a game that is not one": "/api/systems/system01/games/nope",
	} {
		check(name, upload(path, f.gm1, "orders.txt", "orders\n"), http.StatusNotFound)
		check("list "+name, get(path+"/files", f.gm1), http.StatusNotFound)
	}
	check("no token", upload(folder, "", "orders.txt", "orders\n"), http.StatusUnauthorized)
	check("no token, bad file", upload(folder, "", "", ""), http.StatusUnauthorized)

	// Nothing refused was stored.
	if ids := f.uploadIDs(t, game01Path, f.gm1, "?withdrawn=include"); len(ids) != 0 {
		t.Errorf("uploads in game01 after refusals: %v", ids)
	}
	if ids := f.uploadIDs(t, game02Path, f.gm2, "?withdrawn=include"); len(ids) != 0 {
		t.Errorf("uploads in game02 after refusals: %v", ids)
	}
}

// Accepted contents: what the text rules allow, and both body types.
func TestUploadAccepts(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	folder := playerFolder(0, f.catbird)
	for name, contents := range map[string]string{
		"one-byte":   "x",
		"newline":    "\n",
		"tabs-CRLF":  "a\tb\r\nc\r",
		"BOM-text":   bom + "x",
		"non-ASCII":  "naïve 命令 🎲\n",
		"BOM-inside": "a" + bom + "b",
		"html":       "<p>&amp;</p>\n",
	} {
		file := f.put(t, folder, f.gm1, name, contents)
		if w := f.serve(get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, file.ID), f.gm1)); w.Body.String() != contents {
			t.Errorf("%s: downloaded %q, want %q", name, w.Body, contents)
		}
	}
	for _, name := range []string{"a", strings.Repeat("a", 48), "G3.turn-01_Report.TXT", "a..b"} {
		if got := f.put(t, folder, f.gm1, name, "x"); got.Name != name {
			t.Errorf("name %q stored as %q", name, got.Name)
		}
	}
	r := upload(folder, f.gm1, "octets.txt", "x\n")
	r.Header.Set("Content-Type", "application/octet-stream")
	decode[api.File](t, f.fixture, r, http.StatusCreated)
	r = upload(folder, f.gm1, "charset.txt", "x\n")
	r.Header.Set("Content-Type", "text/plain; charset=utf-8")
	decode[api.File](t, f.fixture, r, http.StatusCreated)
}

func TestUploadSizeLimit(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ configured, limit int64 }{
		"default":    {0, 1 << 20},
		"configured": {1000, 1000},
		"raised":     {2 << 20, 2 << 20},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFilesFixture(t, tc.configured)
			atLimit := strings.Repeat("a", int(tc.limit))
			if got := f.put(t, game01Path, f.gm1, "at-limit.txt", atLimit); got.Size != tc.limit {
				t.Errorf("size = %d, want %d", got.Size, tc.limit)
			}
			w := f.serve(upload(game01Path, f.gm1, "over.txt", atLimit+"a"))
			if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), strconv.FormatInt(tc.limit, 10)) {
				t.Errorf("one byte over: %d %s, want 413 naming %d", w.Code, w.Body, tc.limit)
			}
			// A BOM counts toward the limit: the limit is on the bytes stored.
			if w := f.serve(upload(game01Path, f.gm1, "bom.txt", bom+atLimit[3:]+"a")); w.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("BOM pushing the file over: %d", w.Code)
			}
			// A body with no declared length is limited while it is read.
			r := upload(game01Path, f.gm1, "stream.txt", "")
			r.Body = http.NoBody
			r.Body = readCloser{strings.NewReader(atLimit + "a")}
			r.ContentLength = -1
			if w := f.serve(r); w.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("streamed body over the limit: %d", w.Code)
			}
			if got := f.list(t, game01Path, f.gm1); len(got) != 1 {
				t.Errorf("files = %v, want only the one at the limit", got)
			}
		})
	}
}

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }

// A GM adds files whether the turn is open or closed, and in an inactive
// game.
func TestGMUploadsToClosedTurn(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	folder := playerFolder(0, f.catbird)
	decode[api.TurnStatus](t, f.fixture, post(game01Turns+"/0/close", f.gm1, ""), http.StatusOK)
	closed := f.put(t, folder, f.gm1, "report.txt", "closed\n")
	decode[api.TurnStatus](t, f.fixture, post(game01Turns+"/0/open", f.gm1, ""), http.StatusOK)
	f.put(t, folder, f.gm1, "report.txt", "reopened\n")
	decode[api.TurnStatus](t, f.fixture, post(game01Turns+"/0/close", f.gm1, ""), http.StatusOK)
	if got := f.list(t, folder, f.gm1); len(got) != 2 {
		t.Errorf("files = %v, want two", got)
	}
	// A GM withdraws their own file in a closed turn.
	if got := decode[api.File](t, f.fixture, post(fmt.Sprintf("%s/uploads/%d/withdraw", game01Path, closed.ID), f.gm1, ""), http.StatusOK); got.WithdrawnAt == nil {
		t.Errorf("withdraw in a closed turn: %+v", got)
	}
	// A disabled player's folder is still there for the GM.
	decode[api.PlayerStatus](t, f.fixture, post(fmt.Sprintf("%s/%d/disable", game01Players, f.catbird), f.gm1, ""), http.StatusOK)
	f.put(t, folder, f.gm1, "note.txt", "x\n")
	if err := f.s.SetGameActive(t.Context(), game01, false); err != nil {
		t.Fatal(err)
	}
	f.put(t, game01Path, f.gm1, "note.txt", "x\n")
	if got := f.list(t, folder, f.gm1); len(got) != 2 {
		t.Errorf("files in an inactive game = %v, want two", got)
	}
}

func TestWithdraw(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	// acct02 becomes a second GM of game01.
	if err := f.s.AddManager(t.Context(), store.Member{Game: game01, Email: acct02.Email}); err != nil {
		t.Fatal(err)
	}
	folder := playerFolder(0, f.catbird)
	mine := f.put(t, folder, f.gm1, "report.txt", "wrong folder\n")
	keep := f.put(t, folder, f.gm1, "keep.txt", "keep\n")
	theirs := f.put(t, folder, f.gm2, "theirs.txt", "theirs\n")
	withdraw := func(id int64, token string) *http.Request {
		return post(fmt.Sprintf("%s/uploads/%d/withdraw", game01Path, id), token, "")
	}

	f.clock.Advance(time.Hour)
	at := f.clock.Now().Format(api.TimeLayout)
	got := decode[api.File](t, f.fixture, withdraw(mine.ID, f.gm1), http.StatusOK)
	if got.ID != mine.ID || got.WithdrawnAt == nil || *got.WithdrawnAt != at || got.Name != "report.txt" {
		t.Errorf("withdraw = %+v", got)
	}
	// Again: it succeeds and keeps the first time.
	f.clock.Advance(time.Hour)
	if got := decode[api.File](t, f.fixture, withdraw(mine.ID, f.gm1), http.StatusOK); *got.WithdrawnAt != at {
		t.Errorf("second withdraw at %s, want %s", *got.WithdrawnAt, at)
	}
	// Another GM's file, even in a game both manage, is not theirs to withdraw.
	if w := f.serve(withdraw(theirs.ID, f.gm1)); w.Code != http.StatusForbidden || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("withdrawing another GM's file: %d %s", w.Code, w.Body)
	}
	if w := f.serve(withdraw(mine.ID, f.gm2)); w.Code != http.StatusForbidden {
		t.Errorf("another GM withdrawing a withdrawn file: %d", w.Code)
	}
	for _, id := range []int64{0, 999} {
		if w := f.serve(withdraw(id, f.gm1)); w.Code != http.StatusNotFound {
			t.Errorf("withdraw %d: %d, want 404", id, w.Code)
		}
	}

	// The withdrawn file leaves the listings; the others stay.
	want := fmt.Sprintf("%d:keep.txt,%d:theirs.txt", keep.ID, theirs.ID)
	if got := strings.Join(f.list(t, folder, f.gm1), ","); got != want {
		t.Errorf("files = %q, want %q", got, want)
	}
	if got := f.uploadIDs(t, game01Path, f.gm1, ""); len(got) != 2 || got[0] != keep.ID || got[1] != theirs.ID {
		t.Errorf("uploads = %v", got)
	}
	// Its record remains, for those who ask.
	if got := f.uploadIDs(t, game01Path, f.gm1, "?withdrawn=include"); len(got) != 3 || got[0] != mine.ID {
		t.Errorf("uploads with withdrawn = %v", got)
	}
	all := decode[api.FileList](t, f.fixture, get(folder+"/files?withdrawn=include", f.gm1), http.StatusOK).Files
	if len(all) != 3 || all[0].WithdrawnAt == nil || *all[0].WithdrawnAt != at || all[1].WithdrawnAt != nil {
		t.Errorf("files with withdrawn = %+v", all)
	}
	rec := decode[api.File](t, f.fixture, get(fmt.Sprintf("%s/uploads/%d", game01Path, mine.ID), f.gm1), http.StatusOK)
	if rec.WithdrawnAt == nil || rec.UploadedBy != acct01.Email || rec.SHA256 != sha("wrong folder\n") {
		t.Errorf("record of a withdrawn file = %+v", rec)
	}
	// Nothing is deleted: a GM can still read what was uploaded.
	if w := f.serve(get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, mine.ID), f.gm2)); w.Code != http.StatusOK || w.Body.String() != "wrong folder\n" {
		t.Errorf("contents of a withdrawn file: %d %q", w.Code, w.Body)
	}
	for _, q := range []string{"?withdrawn=", "?withdrawn=true", "?withdrawn=exclude"} {
		if w := f.serve(get(folder+"/files"+q, f.gm1)); w.Code != http.StatusBadRequest {
			t.Errorf("files%s: %d, want 400", q, w.Code)
		}
		if w := f.serve(get(game01Path+"/uploads"+q, f.gm1)); w.Code != http.StatusBadRequest {
			t.Errorf("uploads%s: %d, want 400", q, w.Code)
		}
	}
}

// A tool asks for the uploads after the last one it saw.
func TestUploadsAfter(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	if body := f.serve(get(game01Path+"/uploads", f.gm1)).Body.String(); !strings.Contains(body, `"uploads":[]`) {
		t.Errorf("no uploads: %s", body)
	}
	a := f.put(t, game01Path, f.gm1, "a.txt", "a\n")
	other := f.put(t, game02Path, f.gm2, "other.txt", "other\n")
	b := f.put(t, game01Path+"/turns/1", f.gm1, "b.txt", "b\n")
	c := f.put(t, playerFolder(0, f.spoonbill), f.gm1, "c.txt", "c\n")
	for query, want := range map[string][]int64{
		"":                             {a.ID, b.ID, c.ID},
		"?after=0":                     {a.ID, b.ID, c.ID},
		fmt.Sprintf("?after=%d", a.ID): {b.ID, c.ID},
		// An ID from another game is only a position.
		fmt.Sprintf("?after=%d", other.ID): {b.ID, c.ID},
		fmt.Sprintf("?after=%d", c.ID):     {},
		"?after=999999":                    {},
	} {
		if got := f.uploadIDs(t, game01Path, f.gm1, query); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("uploads%s = %v, want %v", query, got, want)
		}
	}
	// Each says where it was put.
	ups := decode[api.UploadList](t, f.fixture, get(game01Path+"/uploads", f.gm1), http.StatusOK).Uploads
	if ups[0].Turn != nil || *ups[1].Turn != 1 || ups[1].Player != nil || *ups[2].Turn != 0 || *ups[2].Player != f.spoonbill {
		t.Errorf("uploads = %+v", ups)
	}
	for _, q := range []string{"?after=", "?after=-1", "?after=abc", "?after=01", "?after=1.5", "?after=" + strings.Repeat("9", 30)} {
		if w := f.serve(get(game01Path+"/uploads"+q, f.gm1)); w.Code != http.StatusBadRequest {
			t.Errorf("uploads%s: %d, want 400", q, w.Code)
		}
	}
	// A new upload appears after the last one seen.
	d := f.put(t, game01Path, f.gm1, "d.txt", "d\n")
	if got := f.uploadIDs(t, game01Path, f.gm1, fmt.Sprintf("?after=%d", c.ID)); len(got) != 1 || got[0] != d.ID {
		t.Errorf("after the last seen = %v, want [%d]", got, d.ID)
	}
}

// An upload is reached only through its own game, and never by its hash.
func TestUploadScope(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	const same = "identical orders\n"
	mine := f.put(t, playerFolder(0, f.catbird), f.gm1, "orders.txt", same)
	also := f.put(t, playerFolder(0, f.spoonbill), f.gm1, "orders.txt", same)
	theirs := f.put(t, fmt.Sprintf("%s/turns/0/players/%d", game02Path, f.caracara), f.gm2, "orders.txt", same)
	if mine.SHA256 != theirs.SHA256 || mine.SHA256 != also.SHA256 || mine.ID == theirs.ID {
		t.Fatalf("records: %+v %+v %+v", mine, also, theirs)
	}

	missing := f.serve(get(game01Path+"/uploads/999999", f.gm1)).Body.String()
	for _, r := range []*http.Request{
		get(fmt.Sprintf("%s/uploads/%d", game01Path, theirs.ID), f.gm1),
		get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, theirs.ID), f.gm1),
		post(fmt.Sprintf("%s/uploads/%d/withdraw", game01Path, theirs.ID), f.gm1, ""),
		get(fmt.Sprintf("%s/uploads/%d", game02Path, theirs.ID), f.gm1),
		get(fmt.Sprintf("%s/uploads/%d/contents", game02Path, theirs.ID), f.gm1),
		post(fmt.Sprintf("%s/uploads/%d/withdraw", game02Path, theirs.ID), f.gm1, ""),
		get(game02Path+"/uploads", f.gm1),
		get(game02Path+"/files", f.gm1),
		// Hashes name nothing.
		get(game01Path+"/uploads/"+mine.SHA256, f.gm1),
		get(game01Path+"/uploads/"+mine.SHA256+"/contents", f.gm1),
		get(game01Path+"/files/"+mine.SHA256, f.gm1),
		get("/api/files/"+mine.SHA256, f.gm1),
		get("/api/contents/"+mine.SHA256, f.gm1),
		get(game01Path+"/uploads/0/contents", f.gm1),
		get(game01Path+"/uploads/-1/contents", f.gm1),
		get(fmt.Sprintf("%s/uploads/0%d/contents", game01Path, mine.ID), f.gm1),
	} {
		w := f.serve(r)
		if w.Code != http.StatusNotFound || w.Body.String() != missing {
			t.Errorf("%s %s: %d %s, want the 404 of a missing upload", r.Method, r.URL.Path, w.Code, w.Body)
		}
	}
	// Withdrawing one upload leaves the others of the same contents.
	decode[api.File](t, f.fixture, post(fmt.Sprintf("%s/uploads/%d/withdraw", game01Path, mine.ID), f.gm1, ""), http.StatusOK)
	if got := f.list(t, playerFolder(0, f.spoonbill), f.gm1); len(got) != 1 {
		t.Errorf("spoonbill's files = %v", got)
	}
	if w := f.serve(get(fmt.Sprintf("%s/uploads/%d/contents", game02Path, theirs.ID), f.gm2)); w.Code != http.StatusOK || w.Body.String() != same {
		t.Errorf("the other game's copy: %d %q", w.Code, w.Body)
	}
	if rec := decode[api.File](t, f.fixture, get(fmt.Sprintf("%s/uploads/%d", game02Path, theirs.ID), f.gm2), http.StatusOK); rec.WithdrawnAt != nil {
		t.Errorf("the other game's copy was withdrawn: %+v", rec)
	}

	// A player's account is not a GM: the file routes of its game are 404.
	if _, err := f.s.AddPlayer(t.Context(), store.Member{Game: game01, Email: acct02.Email}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*http.Request{
		get(game01Path+"/files", f.gm2),
		get(game01Path+"/uploads", f.gm2),
		get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, also.ID), f.gm2),
		upload(game01Path, f.gm2, "x.txt", "x\n"),
	} {
		if w := f.serve(r); w.Code != http.StatusNotFound {
			t.Errorf("%s %s as a player: %d, want 404", r.Method, r.URL.Path, w.Code)
		}
	}
	// A disabled GM loses the files.
	if err := f.s.SetManagerActive(t.Context(), store.Member{Game: game01, Email: acct01.Email}, false); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, also.ID), f.gm1)); w.Code != http.StatusNotFound {
		t.Errorf("download as a disabled GM: %d", w.Code)
	}
}

// Identical files uploaded at once are all recorded.
func TestConcurrentIdenticalUploads(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	const n = 12
	contents := bytes.Repeat([]byte("same orders\n"), 500)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			path, token := playerFolder(0, f.catbird), f.gm1
			if i%2 == 1 {
				path, token = fmt.Sprintf("%s/turns/0/players/%d", game02Path, f.caracara), f.gm2
			}
			if w := f.serve(upload(path, token, "orders.txt", string(contents))); w.Code != http.StatusCreated {
				t.Errorf("upload %d: %d %s", i, w.Code, w.Body)
			}
		})
	}
	wg.Wait()
	ids := append(f.uploadIDs(t, game01Path, f.gm1, ""), f.uploadIDs(t, game02Path, f.gm2, "")...)
	if len(ids) != n {
		t.Fatalf("%d uploads recorded, want %d", len(ids), n)
	}
	w := f.serve(get(fmt.Sprintf("%s/uploads/%d/contents", game01Path, f.uploadIDs(t, game01Path, f.gm1, "")[0]), f.gm1))
	if !bytes.Equal(w.Body.Bytes(), contents) {
		t.Error("contents differ after concurrent uploads")
	}
}

func TestFileRouteMethods(t *testing.T) {
	t.Parallel()
	f := newFilesFixture(t, 0)
	file := f.put(t, game01Path, f.gm1, "a.txt", "a\n")
	record := fmt.Sprintf("%s/uploads/%d", game01Path, file.ID)
	for path, allow := range map[string]string{
		game01Path + "/files":   "GET, HEAD, POST",
		game01Path + "/uploads": "GET, HEAD",
		record:                  "GET, HEAD",
		record + "/contents":    "GET, HEAD",
	} {
		r := httptest.NewRequest(http.MethodDelete, path, nil)
		r.Header.Set("Authorization", "Bearer "+f.gm1)
		w := f.serve(r)
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != allow || w.Header().Get("Content-Type") != "application/json" {
			t.Errorf("DELETE %s: %d Allow %q %s", path, w.Code, w.Header().Get("Allow"), w.Body)
		}
	}
	// Nothing deletes a file.
	if got := f.list(t, game01Path, f.gm1); len(got) != 1 {
		t.Errorf("files = %v", got)
	}
	r := httptest.NewRequest(http.MethodHead, record+"/contents", nil)
	r.Header.Set("Authorization", "Bearer "+f.gm1)
	if w := f.serve(r); w.Code != http.StatusOK || w.Header().Get("X-Content-SHA256") != file.SHA256 {
		t.Errorf("HEAD contents: %d %v", w.Code, w.Header())
	}
}
