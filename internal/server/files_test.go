// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/server"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	game02Path = "/systems/system01/games/game02"
	// The default players' numbers, in the order devdata adds them.
	catNo, spoonNo, caraNo = 1, 2, 3
)

// pagesFixture is a server with turns 0 and 1 in game01 and turn 0 in
// game02, and every default account signed in: the GMs of game01 and
// game02, and the players catbird and spoonbill (game01) and caracara
// (game02), who have chosen passwords through their invitations.
type pagesFixture struct {
	*accountFixture
	gm, gm2, cat, spoon, cara *http.Cookie
	game01, game02            int64
}

func newPagesFixture(t *testing.T, adjust func(*server.Config)) *pagesFixture {
	t.Helper()
	f := &pagesFixture{accountFixture: newConfiguredFixture(t, adjust)}
	ctx := t.Context()
	f.game01, _, _ = f.s.GameID(ctx, store.GameRef{System: "system01", Code: "game01"})
	f.game02, _, _ = f.s.GameID(ctx, store.GameRef{System: "system01", Code: "game02"})
	for _, turn := range []struct{ game, number int64 }{{f.game01, 0}, {f.game01, 1}, {f.game02, 0}} {
		if _, err := f.s.CreateTurn(ctx, turn.game, turn.number, f.now); err != nil {
			t.Fatal(err)
		}
	}
	f.gm = f.signIn(t, acct01.Email, acct01.Password, "")
	f.gm2 = f.signIn(t, acct02.Email, acct02.Password, "")
	f.cat = f.join(t, "catbird@example.com")
	f.spoon = f.join(t, "spoonbill@example.com")
	f.cara = f.join(t, "caracara@example.com")
	f.mail.Reset()
	return f
}

// join has the account choose a password through a mailed link, and signs
// it in. The password is made up for the test.
func (f *accountFixture) join(t *testing.T, email string) *http.Cookie {
	t.Helper()
	password := rand.Text()
	if w := f.askReset(email, ""); w.Code != http.StatusOK {
		t.Fatalf("ask for a link for %s: %d", email, w.Code)
	}
	_, token := f.lastLink(t)
	if w := f.setPassword(token, password, password); w.Code != http.StatusOK {
		t.Fatalf("set the password of %s: %d", email, w.Code)
	}
	return f.signIn(t, email, password, "")
}

// uploadRequest is the request a drop zone sends: a same-origin HTMX post
// of a multipart form holding the file.
func uploadRequest(t *testing.T, folder, name string, contents []byte, cookie *http.Cookie) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	part.Write(contents)
	form.Close()
	r := httptest.NewRequest(http.MethodPost, publicOrigin+folder+"/files", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("Origin", publicOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("HX-Request", "true")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func (f *accountFixture) upload(t *testing.T, folder, name string, contents []byte, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return serve(f.h, uploadRequest(t, folder, name, contents, cookie))
}

// post sends a same-origin form, as HTMX or as a plain form.
func (f *accountFixture) post(path string, values url.Values, cookie *http.Cookie, htmx bool) *httptest.ResponseRecorder {
	r := form(path, "", values)
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return serve(f.h, r)
}

// uploads returns every upload of the game, withdrawn or not.
func (f *accountFixture) uploads(t *testing.T, game int64) []store.Upload {
	t.Helper()
	uploads, err := f.s.GameUploads(t.Context(), game, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	return uploads
}

// mustUpload uploads the file and returns its ID.
func (f *pagesFixture) mustUpload(t *testing.T, folder, name string, contents []byte, cookie *http.Cookie) int64 {
	t.Helper()
	if w := f.upload(t, folder, name, contents, cookie); w.Code != http.StatusCreated {
		t.Fatalf("upload %s to %s: %d %s", name, folder, w.Code, w.Body)
	}
	game := f.game01
	if strings.HasPrefix(folder, game02Path) {
		game = f.game02
	}
	all := f.uploads(t, game)
	return all[len(all)-1].ID
}

func folderPath(game string, turn, player int) string {
	return fmt.Sprintf("%s/turns/%d/players/%d", game, turn, player)
}

func uploadPath(game string, id int64) string {
	return fmt.Sprintf("%s/uploads/%d", game, id)
}

func listed(body, name string) bool {
	return strings.Contains(body, ">"+name+"</a>")
}

// An account sees only the games it may reach, and lands in its only one.
func TestHomeListsReachableGames(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	for who, tc := range map[string]struct {
		cookie *http.Cookie
		game   string
	}{"GM": {f.gm, game01Path}, "player": {f.cat, game01Path}, "other player": {f.cara, game02Path}} {
		if w := f.get("/", tc.cookie); w.Code != http.StatusSeeOther || w.Header().Get("Location") != tc.game {
			t.Errorf("%s: GET / = %d, Location %q; want %s", who, w.Code, w.Header().Get("Location"), tc.game)
		}
	}
	// With two games the account gets the list: here as a GM of one and a
	// player in the other.
	if _, err := f.s.AddPlayer(t.Context(), store.Member{Game: store.GameRef{System: "system01", Code: "game02"}, Email: acct01.Email}); err != nil {
		t.Fatal(err)
	}
	w := f.get("/", f.gm)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `href="`+game01Path+`"`) || !strings.Contains(body, `href="`+game02Path+`"`) ||
		!strings.Contains(body, "GM") || !strings.Contains(body, "Player") {
		t.Errorf("home with two games: %d %s", w.Code, body)
	}
	if w := f.get("/systems/system01", f.gm); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="`+game02Path+`"`) {
		t.Errorf("system page: %d", w.Code)
	}
	// The other GM sees only their own game there.
	if w := f.get("/systems/system01", f.gm2); w.Code != http.StatusOK || strings.Contains(w.Body.String(), game01Path) {
		t.Errorf("system page shows a game the account may not reach: %d", w.Code)
	}
	// An account in no game is told so, and no system exists for it.
	if err := f.s.SetPlayerActive(t.Context(), store.Member{Game: store.GameRef{System: "system01", Code: "game01"}, Email: "catbird@example.com"}, false); err != nil {
		t.Fatal(err)
	}
	if w := f.get("/", f.cat); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "not in any game") || strings.Contains(w.Body.String(), "game01") {
		t.Errorf("home with no games: %d %s", w.Code, w.Body)
	}
	if w := f.get("/systems/system01", f.cat); w.Code != http.StatusNotFound {
		t.Errorf("system page with no games: %d", w.Code)
	}
}

// Every page and every action needs a session.
func TestPagesNeedASession(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	id := f.mustUpload(t, game01Path, "rules.txt", []byte("rules\n"), f.gm)
	for _, path := range []string{"/", "/systems/system01", game01Path, game01Path + "/turns/0", folderPath(game01Path, 0, catNo), game01Path + "/players", uploadPath(game01Path, id), "/systems/nope/games/nope"} {
		for what, cookie := range map[string]*http.Cookie{"no cookie": nil, "a made-up cookie": {Name: server.SessionCookie, Value: "nonsense"}} {
			w := f.get(path, cookie)
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != server.SignInPath || strings.Contains(w.Body.String(), "rules") {
				t.Errorf("GET %s with %s: %d, Location %q", path, what, w.Code, w.Header().Get("Location"))
			}
		}
	}
	before := len(f.uploads(t, f.game01))
	// HTMX is told to send the whole browser to the sign-in page.
	w := f.upload(t, game01Path, "more.txt", []byte("more\n"), nil)
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != server.SignInPath || w.Body.Len() != 0 {
		t.Errorf("upload without a session: %d, HX-Redirect %q, body %q", w.Code, w.Header().Get("HX-Redirect"), w.Body)
	}
	for _, path := range []string{uploadPath(game01Path, id) + "/withdraw", game01Path + "/turns", game01Path + "/turns/0/close", game01Path + "/players"} {
		if w := f.post(path, url.Values{"turn": {"2"}, "email": {"new@example.com"}}, nil, false); w.Code != http.StatusSeeOther || w.Header().Get("Location") != server.SignInPath {
			t.Errorf("POST %s without a session: %d", path, w.Code)
		}
	}
	turns, _ := f.s.Turns(t.Context(), f.game01)
	if len(f.uploads(t, f.game01)) != before || f.uploads(t, f.game01)[0].Withdrawn() || len(turns) != 2 || turns[0].Closed() || len(f.mail.Messages()) != 0 {
		t.Error("a request without a session changed something")
	}
}

// What an account may not reach is indistinguishable from what does not
// exist.
func TestUnreachableIsNotFound(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	mine := f.mustUpload(t, folderPath(game01Path, 0, catNo), "orders.txt", []byte("cat's orders\n"), f.cat)
	theirs := f.mustUpload(t, folderPath(game02Path, 0, caraNo), "orders.txt", []byte("cara's orders\n"), f.cara)

	missing := f.get("/systems/system01/games/nope", f.cat)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "nothing here") {
		t.Fatalf("a game that does not exist: %d %s", missing.Code, missing.Body)
	}
	same := func(who string, cookie *http.Cookie, path string) {
		t.Helper()
		w := f.get(path, cookie)
		// The header names the account, so the bodies are compared without it.
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "nothing here") || strings.Contains(w.Body.String(), "orders") {
			t.Errorf("%s: GET %s = %d %s", who, path, w.Code, w.Body)
		}
	}
	for who, tc := range map[string]struct {
		cookie *http.Cookie
		paths  []string
	}{
		"a player, another game": {f.cat, []string{game02Path, game02Path + "/turns/0", folderPath(game02Path, 0, caraNo), uploadPath(game02Path, theirs), "/systems/nope"}},
		// Another player's folder, and that it exists; a turn and a
		// player the game does not have; an upload of another game
		// addressed through this one; the GM's pages.
		"a player, their own game": {f.spoon, []string{folderPath(game01Path, 0, catNo), uploadPath(game01Path, mine), game01Path + "/turns/7", folderPath(game01Path, 0, 99), folderPath(game01Path, 0, caraNo), uploadPath(game01Path, theirs), game01Path + "/players", game01Path + "/turns/00", game01Path + "/turns/-1", uploadPath(game01Path, 0)}},
		"a GM, another game":       {f.gm, []string{game02Path, game02Path + "/turns/0", folderPath(game02Path, 0, caraNo), uploadPath(game02Path, theirs), game02Path + "/players", uploadPath(game01Path, theirs), folderPath(game01Path, 0, caraNo)}},
	} {
		for _, path := range tc.paths {
			same(who, tc.cookie, path)
		}
	}
	// A player's turn page says nothing of the other players.
	w := f.get(game01Path+"/turns/0", f.spoon)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "catbird") || strings.Contains(w.Body.String(), "/players/1") || strings.Contains(w.Body.String(), "cat's") {
		t.Errorf("a player's turn page shows another player: %d %s", w.Code, w.Body)
	}
	// Actions on what cannot be reached answer the same way, and do nothing.
	for who, cookie := range map[string]*http.Cookie{"another player": f.spoon, "another game's GM": f.gm2} {
		if w := f.post(uploadPath(game01Path, mine)+"/withdraw", nil, cookie, false); w.Code != http.StatusNotFound {
			t.Errorf("%s withdraws: %d", who, w.Code)
		}
		if w := f.upload(t, folderPath(game01Path, 0, catNo), "x.txt", []byte("x\n"), cookie); w.Code != http.StatusNotFound || w.Header().Get("HX-Refresh") != "true" {
			t.Errorf("%s uploads to the folder: %d", who, w.Code)
		}
	}
	if all := f.uploads(t, f.game01); len(all) != 1 || all[0].Withdrawn() {
		t.Errorf("refused requests changed the uploads: %+v", all)
	}
}

// A player drops their orders on their folder in an open turn, sees where
// they went, and gets back exactly what they sent.
func TestPlayerUploadsOrders(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	turn := game01Path + "/turns/1"
	folder := folderPath(game01Path, 1, catNo)

	// Before the drop, the page names the game and the turn of the folder.
	page := f.get(turn, f.cat).Body.String()
	for _, want := range []string{`id="files-player"`, `hx-post="` + folder + `/files"`, `hx-encoding="multipart/form-data"`, `type="file"`, `data-max="65536"`, "system01 / game01 / turn 1 / your folder", "Open"} {
		if !strings.Contains(page, want) {
			t.Errorf("the turn page does not contain %s", want)
		}
	}
	// There is no Upload button, and a player has no drop zone on the
	// turn's own files, nor on the game's.
	if strings.Contains(page, ">Upload<") || strings.Count(page, "data-drop ") != 1 || strings.Contains(f.get(game01Path, f.cat).Body.String(), "data-drop ") {
		t.Error("a player is offered a drop zone other than their folder's")
	}

	orders := []byte("\xef\xbb\xbfmove 1 north\r\nrécolte\r\n")
	w := f.upload(t, folder, "orders.txt", orders, f.cat)
	body := w.Body.String()
	if w.Code != http.StatusCreated || !strings.HasPrefix(strings.TrimSpace(body), `<section id="files-player"`) || strings.Contains(body, "<html") {
		t.Fatalf("upload: %d %s", w.Code, body)
	}
	// The receipt names the file, the game, and the turn.
	for _, want := range []string{"data-receipt", "Received", "orders.txt", "system01 / game01 / turn 1 / your folder", fmt.Sprintf("%d bytes", len(orders)), "you"} {
		if !strings.Contains(body, want) {
			t.Errorf("the receipt does not contain %q: %s", want, body)
		}
	}
	all := f.uploads(t, f.game01)
	if len(all) != 1 || *all[0].Turn != 1 || *all[0].Player != catNo || all[0].Name != "orders.txt" || all[0].UploaderEmail != "catbird@example.com" || !all[0].UploadedAt.Equal(f.now) {
		t.Fatalf("stored upload = %+v", all)
	}
	// A second file with the same name replaces nothing.
	second := f.mustUpload(t, folder, "orders.txt", []byte("move 1 south\n"), f.cat)
	if page := f.get(turn, f.cat).Body.String(); strings.Count(page, ">orders.txt</a>") != 2 {
		t.Errorf("the folder does not list both files: %s", page)
	}

	// The download is the stored file, byte for byte, and cannot run here.
	d := f.get(uploadPath(game01Path, all[0].ID), f.cat)
	if d.Code != http.StatusOK || !bytes.Equal(d.Body.Bytes(), orders) {
		t.Fatalf("download: %d %q", d.Code, d.Body)
	}
	for header, want := range map[string]string{
		"Content-Type": "application/octet-stream", "Content-Disposition": `attachment; filename="orders.txt"`,
		"X-Content-Type-Options": "nosniff", "Content-Security-Policy": "sandbox; default-src 'none'", "Cache-Control": "private, no-store",
	} {
		if got := d.Header().Get(header); got != want {
			t.Errorf("download %s = %q, want %q", header, got, want)
		}
	}
	// The GM reads the folder, sees who sent the file, and may add to it.
	gmPage := f.get(folder, f.gm).Body.String()
	if !listed(gmPage, "orders.txt") || !strings.Contains(gmPage, "catbird@example.com") || !strings.Contains(gmPage, `data-max="1048576"`) {
		t.Errorf("the GM's view of the folder: %s", gmPage)
	}
	if !strings.Contains(f.get(turn, f.gm).Body.String(), "2 files") {
		t.Error("the GM's turn page does not count the folder's files")
	}
	if d := f.get(uploadPath(game01Path, second), f.gm); d.Code != http.StatusOK || d.Body.String() != "move 1 south\n" {
		t.Errorf("the GM's download: %d", d.Code)
	}
	// A report the GM puts in the folder is the player's to read, and the
	// player is not shown the GM's address.
	report := f.mustUpload(t, folder, "report.html", []byte("<script>alert(1)</script>\n"), f.gm)
	page = f.get(turn, f.cat).Body.String()
	if !listed(page, "report.html") || strings.Contains(page, acct01.Email) || !strings.Contains(page, "the GM") {
		t.Errorf("the player's view of the GM's report: %s", page)
	}
	if d := f.get(uploadPath(game01Path, report), f.cat); d.Code != http.StatusOK || d.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("the player's download of the report: %d", d.Code)
	}
	// So are the turn's files and the game's, which only the GM adds.
	f.mustUpload(t, turn, "news.txt", []byte("news\n"), f.gm)
	f.mustUpload(t, game01Path, "rules.txt", []byte("rules\n"), f.gm)
	if !listed(f.get(turn, f.spoon).Body.String(), "news.txt") || !listed(f.get(game01Path, f.spoon).Body.String(), "rules.txt") {
		t.Error("a player does not see the turn's and the game's files")
	}
	for _, level := range []string{turn, game01Path} {
		if w := f.upload(t, level, "mine.txt", []byte("mine\n"), f.cat); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "data-error") {
			t.Errorf("a player uploads to %s: %d", level, w.Code)
		}
	}
	// A plain form post, without HTMX, is sent back to the folder's page.
	r := uploadRequest(t, folder, "plain.txt", []byte("plain\n"), f.cat)
	r.Header.Del("HX-Request")
	if w := serve(f.h, r); w.Code != http.StatusSeeOther || w.Header().Get("Location") != turn {
		t.Errorf("a plain upload: %d, Location %q", w.Code, w.Header().Get("Location"))
	}
	// No file's contents, and no session, are logged.
	logs := f.logs.String()
	for _, secret := range []string{"move 1", "alert(1)", f.cat.Value, f.gm.Value} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
	if !strings.Contains(logs, "uploaded file") || !strings.Contains(logs, "orders.txt") {
		t.Error("the upload was not logged")
	}
}

// Closing a turn stops its players adding and withdrawing, and nothing
// else. Any number of turns may be open.
func TestClosedTurnStopsOnlyPlayers(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	turn0, folder0, folder1 := game01Path+"/turns/0", folderPath(game01Path, 0, catNo), folderPath(game01Path, 1, catNo)
	// Both turns are open: a player who will be away sends orders to each.
	early := f.mustUpload(t, folder0, "orders.txt", []byte("turn 0\n"), f.cat)
	f.mustUpload(t, folder1, "orders.txt", []byte("turn 1\n"), f.cat)
	gmFile := f.mustUpload(t, folder0, "report.txt", []byte("report\n"), f.gm)

	if w := f.post(turn0+"/close", nil, f.gm, false); w.Code != http.StatusSeeOther || w.Header().Get("Location") != turn0 {
		t.Fatalf("close: %d", w.Code)
	}
	page := f.get(turn0, f.cat).Body.String()
	if strings.Contains(page, "data-drop ") || !strings.Contains(page, "data-closed") || !strings.Contains(page, "Closed") || strings.Contains(page, "/withdraw") {
		t.Errorf("a closed turn offers a player a drop zone or a withdrawal: %s", page)
	}
	// The player still reads the folder.
	if !listed(page, "orders.txt") || f.get(uploadPath(game01Path, early), f.cat).Code != http.StatusOK {
		t.Error("a player cannot read their folder in a closed turn")
	}
	before := len(f.uploads(t, f.game01))
	w := f.upload(t, folder0, "late.txt", []byte("late\n"), f.cat)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Turn 0 is closed") || strings.Contains(w.Body.String(), "data-drop ") {
		t.Errorf("upload to a closed turn: %d %s", w.Code, w.Body)
	}
	if w := f.post(uploadPath(game01Path, early)+"/withdraw", nil, f.cat, true); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "closed") {
		t.Errorf("withdraw from a closed turn: %d %s", w.Code, w.Body)
	}
	if all := f.uploads(t, f.game01); len(all) != before || all[0].Withdrawn() {
		t.Error("a closed turn's folder changed")
	}
	// The other turn is still open.
	f.mustUpload(t, folder1, "more.txt", []byte("more\n"), f.cat)
	// The GM still adds to the closed turn, and withdraws their own file.
	f.mustUpload(t, folder0, "late-report.txt", []byte("late\n"), f.gm)
	f.mustUpload(t, turn0, "summary.txt", []byte("summary\n"), f.gm)
	if w := f.post(uploadPath(game01Path, gmFile)+"/withdraw", nil, f.gm, true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Withdrew report.txt") {
		t.Errorf("the GM withdraws in a closed turn: %d %s", w.Code, w.Body)
	}
	// Reopened, the turn takes the player's files again.
	if w := f.post(turn0+"/open", nil, f.gm, false); w.Code != http.StatusSeeOther {
		t.Fatalf("reopen: %d", w.Code)
	}
	f.mustUpload(t, folder0, "late.txt", []byte("late\n"), f.cat)
	if w := f.post(uploadPath(game01Path, early)+"/withdraw", nil, f.cat, true); w.Code != http.StatusOK {
		t.Errorf("withdraw from a reopened turn: %d", w.Code)
	}
	// Nothing goes to a turn that does not exist.
	before = len(f.uploads(t, f.game01))
	for who, cookie := range map[string]*http.Cookie{"player": f.cat, "GM": f.gm} {
		for _, folder := range []string{game01Path + "/turns/2", folderPath(game01Path, 2, catNo)} {
			if w := f.upload(t, folder, "orders.txt", []byte("x\n"), cookie); w.Code != http.StatusNotFound {
				t.Errorf("%s uploads to %s: %d", who, folder, w.Code)
			}
		}
	}
	if len(f.uploads(t, f.game01)) != before {
		t.Error("an upload to a missing turn was stored")
	}
}

// Each limit is enforced at its boundary and independently of the others.
func TestWebUploadLimits(t *testing.T) {
	t.Parallel()
	text := func(n int) []byte { return bytes.Repeat([]byte("a"), n) }
	check := func(t *testing.T, f *pagesFixture, player, gm int) {
		t.Helper()
		folder := folderPath(game01Path, 0, catNo)
		for who, tc := range map[string]struct {
			cookie *http.Cookie
			limit  int
		}{"player": {f.cat, player}, "GM": {f.gm, gm}} {
			if w := f.upload(t, folder, "at.txt", text(tc.limit), tc.cookie); w.Code != http.StatusCreated {
				t.Errorf("%s uploads %d bytes: %d", who, tc.limit, w.Code)
			}
			before := len(f.uploads(t, f.game01))
			w := f.upload(t, folder, "over.txt", text(tc.limit+1), tc.cookie)
			if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "larger than the limit") {
				t.Errorf("%s uploads %d bytes: %d %s", who, tc.limit+1, w.Code, w.Body)
			}
			// Far over the limit, the request is cut off as it is read.
			if w := f.upload(t, folder, "huge.txt", text(tc.limit+1<<20), tc.cookie); w.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("%s uploads far too much: %d", who, w.Code)
			}
			if len(f.uploads(t, f.game01)) != before {
				t.Errorf("%s: a file over the limit was stored", who)
			}
		}
	}
	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		check(t, newPagesFixture(t, nil), 64<<10, 1<<20)
	})
	t.Run("configured", func(t *testing.T) {
		t.Parallel()
		f := newPagesFixture(t, func(cfg *server.Config) { cfg.MaxPlayerUpload, cfg.MaxGMUpload, cfg.MaxAPIUpload = 100, 300, 200 })
		check(t, f, 100, 300)
		if page := f.get(game01Path+"/turns/0", f.cat).Body.String(); !strings.Contains(page, `data-max="100"`) || !strings.Contains(page, "100 bytes") {
			t.Error("the drop zone does not give the configured limit")
		}
	})
	// A player's limit above the GM's is still each one's own.
	t.Run("player above GM", func(t *testing.T) {
		t.Parallel()
		check(t, newPagesFixture(t, func(cfg *server.Config) { cfg.MaxPlayerUpload, cfg.MaxGMUpload = 500, 50 }), 500, 50)
	})
	// Lowering a limit leaves stored files readable.
	t.Run("lowered", func(t *testing.T) {
		t.Parallel()
		f := newPagesFixture(t, func(cfg *server.Config) { cfg.MaxPlayerUpload = 1000 })
		id := f.mustUpload(t, folderPath(game01Path, 0, catNo), "big.txt", text(1000), f.cat)
		small, err := server.New(server.Config{Env: "production", Store: f.s, Logger: slog.New(slog.DiscardHandler), MaxPlayerUpload: 10, Now: func() time.Time { return f.now }})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, publicOrigin+uploadPath(game01Path, id), nil)
		r.AddCookie(f.cat)
		if w := serve(small, r); w.Code != http.StatusOK || w.Body.Len() != 1000 {
			t.Errorf("download after lowering the limit: %d, %d bytes", w.Code, w.Body.Len())
		}
	})
}

// Whoever uploads, a file must be text with a safe name.
func TestWebUploadChecks(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	folder := folderPath(game01Path, 0, catNo)
	good := []byte("orders\n")
	for who, cookie := range map[string]*http.Cookie{"player": f.cat, "GM": f.gm} {
		for what, tc := range map[string]struct {
			name     string
			contents []byte
		}{
			"empty":                   {"a.txt", nil},
			"only a BOM":              {"a.txt", []byte("\xef\xbb\xbf")},
			"invalid UTF-8 at start":  {"a.txt", []byte("\xffabc")},
			"invalid UTF-8 in middle": {"a.txt", []byte("ab\xc3(cd")},
			"invalid UTF-8 at end":    {"a.txt", []byte("abc\xe2\x82")},
			"NUL":                     {"a.txt", []byte("a\x00b")},
			"escape":                  {"a.txt", []byte("a\x1bb")},
			"space in name":           {"my orders.txt", good},
			"leading period":          {".orders", good},
			"trailing period":         {"orders.", good},
			"49-byte name":            {strings.Repeat("a", 49), good},
			"non-ASCII name":          {"órdenes.txt", good},
			"angle bracket in name":   {"a<b.txt", good},
			"ampersand in name":       {"a&b.txt", good},
			"semicolon in name":       {"a;b.txt", good},
			"quote in name":           {"a'b.txt", good},
			"backslash in name":       {`a\b.txt`, good},
			"no name":                 {"", good},
		} {
			w := f.upload(t, folder, tc.name, tc.contents, cookie)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "data-error") || !strings.Contains(w.Body.String(), "not accepted") {
				t.Errorf("%s, %s: %d %s", who, what, w.Code, w.Body)
			}
		}
		// A request that is not the drop zone's form is refused too.
		r := form(folder+"/files", "", url.Values{"file": {"orders"}})
		r.Header.Set("HX-Request", "true")
		r.AddCookie(cookie)
		if w := serve(f.h, r); w.Code != http.StatusBadRequest {
			t.Errorf("%s, not multipart: %d", who, w.Code)
		}
		var body bytes.Buffer
		mp := multipart.NewWriter(&body)
		mp.WriteField("other", "x")
		mp.Close()
		r = httptest.NewRequest(http.MethodPost, publicOrigin+folder+"/files", &body)
		r.Header.Set("Content-Type", mp.FormDataContentType())
		r.Header.Set("HX-Request", "true")
		r.AddCookie(cookie)
		if w := serve(f.h, r); w.Code != http.StatusBadRequest {
			t.Errorf("%s, no file part: %d", who, w.Code)
		}
	}
	if all := f.uploads(t, f.game01); len(all) != 0 {
		t.Fatalf("refused files were stored: %+v", all)
	}
	// What is allowed: a BOM before text, bytes above 0x7F, tab, LF, and
	// CR, and a 48-byte name.
	for who, cookie := range map[string]*http.Cookie{"player": f.cat, "GM": f.gm} {
		for name, contents := range map[string][]byte{
			"bom.txt":               []byte("\xef\xbb\xbforders\n"),
			"utf8.txt":              []byte("naïve — 日本語\n"),
			"white-space.txt":       []byte("a\tb\r\nc\rd\n"),
			strings.Repeat("a", 48): good,
			"Orders_2.final-v3.TXT": good,
		} {
			if w := f.upload(t, folder, name, contents, cookie); w.Code != http.StatusCreated {
				t.Errorf("%s, %s: %d %s", who, name, w.Code, w.Body)
			}
		}
	}
}

// A withdrawn file leaves the player's lists; its record stays for the GM.
func TestWebWithdraw(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	turn, folder := game01Path+"/turns/0", folderPath(game01Path, 0, catNo)
	orders := f.mustUpload(t, folder, "orders.txt", []byte("orders\n"), f.cat)
	report := f.mustUpload(t, folder, "report.txt", []byte("report\n"), f.gm)
	news := f.mustUpload(t, turn, "news.txt", []byte("news\n"), f.gm)
	withdraw := func(id int64, cookie *http.Cookie) *httptest.ResponseRecorder {
		return f.post(uploadPath(game01Path, id)+"/withdraw", nil, cookie, true)
	}
	// Only the uploader withdraws a file: not the GM a player's, nor a
	// player the GM's.
	if w := withdraw(orders, f.gm); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "data-error") {
		t.Errorf("the GM withdraws a player's file: %d", w.Code)
	}
	for what, id := range map[string]int64{"in their folder": report, "in the turn": news} {
		if w := withdraw(id, f.cat); w.Code != http.StatusForbidden {
			t.Errorf("a player withdraws the GM's file %s: %d", what, w.Code)
		}
	}
	page := f.get(turn, f.cat).Body.String()
	if strings.Count(page, "/withdraw") != 2 || !strings.Contains(page, fmt.Sprintf(`hx-post="%s/withdraw"`, uploadPath(game01Path, orders))) {
		t.Errorf("a player is offered withdrawals other than their own file's: %s", page)
	}
	for _, u := range f.uploads(t, f.game01) {
		if u.Withdrawn() {
			t.Fatalf("a refused withdrawal withdrew %s", u.Name)
		}
	}

	f.now = f.now.Add(1)
	w := withdraw(orders, f.cat)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Withdrew orders.txt") || listed(body, "orders.txt") || !listed(body, "report.txt") {
		t.Fatalf("withdraw: %d %s", w.Code, body)
	}
	// To the player it is gone, like a file that never was.
	if listed(f.get(turn, f.cat).Body.String(), "orders.txt") {
		t.Error("a withdrawn file is still listed for the player")
	}
	if w := f.get(uploadPath(game01Path, orders), f.cat); w.Code != http.StatusNotFound {
		t.Errorf("the player downloads a withdrawn file: %d", w.Code)
	}
	if w := withdraw(orders, f.cat); w.Code != http.StatusNotFound {
		t.Errorf("the player withdraws a withdrawn file: %d", w.Code)
	}
	// The GM still sees what was uploaded and when, and can read it.
	gmPage := f.get(folder, f.gm).Body.String()
	if !listed(gmPage, "orders.txt") || !strings.Contains(gmPage, "data-withdrawn") || !strings.Contains(gmPage, "withdrawn 2026-11-22") {
		t.Errorf("the GM's view of a withdrawn file: %s", gmPage)
	}
	if w := f.get(uploadPath(game01Path, orders), f.gm); w.Code != http.StatusOK || w.Body.String() != "orders\n" {
		t.Errorf("the GM downloads a withdrawn file: %d", w.Code)
	}
	// It is not counted as one of the folder's files.
	if !strings.Contains(f.get(turn, f.gm).Body.String(), "1 file<") {
		t.Error("the withdrawn file is counted")
	}
	// The GM withdraws a report put in the wrong folder.
	if w := withdraw(report, f.gm); w.Code != http.StatusOK {
		t.Errorf("the GM withdraws their own file: %d", w.Code)
	}
	if listed(f.get(turn, f.cat).Body.String(), "report.txt") {
		t.Error("the GM's withdrawn report is still listed for the player")
	}
	// Nothing was deleted.
	if all := f.uploads(t, f.game01); len(all) != 3 || !all[0].Withdrawn() || !all[1].Withdrawn() || all[2].Withdrawn() {
		t.Errorf("uploads after withdrawing: %+v", all)
	}
	// A plain form post is sent back to the page.
	if w := f.post(uploadPath(game01Path, news)+"/withdraw", nil, f.gm, false); w.Code != http.StatusSeeOther || w.Header().Get("Location") != turn {
		t.Errorf("a plain withdrawal: %d, Location %q", w.Code, w.Header().Get("Location"))
	}
}

// An inactive account, player, GM, or game is refused.
func TestInactiveIsRefused(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	ctx := t.Context()
	game := store.GameRef{System: "system01", Code: "game01"}
	folder := folderPath(game01Path, 0, catNo)
	id := f.mustUpload(t, folder, "orders.txt", []byte("orders\n"), f.cat)
	refused := func(what string, cookie *http.Cookie, status int) {
		t.Helper()
		for _, path := range []string{game01Path, game01Path + "/turns/0", folder, uploadPath(game01Path, id)} {
			if w := f.get(path, cookie); w.Code != status {
				t.Errorf("%s: GET %s = %d, want %d", what, path, w.Code, status)
			}
		}
		if w := f.upload(t, folder, "more.txt", []byte("more\n"), cookie); w.Code == http.StatusCreated {
			t.Errorf("%s: an upload was accepted", what)
		}
	}
	cat := store.Member{Game: game, Email: "catbird@example.com"}
	if err := f.s.SetPlayerActive(ctx, cat, false); err != nil {
		t.Fatal(err)
	}
	refused("inactive player", f.cat, http.StatusNotFound)
	f.s.SetPlayerActive(ctx, cat, true)

	// An inactive game is closed to its players, not to its GM.
	if err := f.s.SetGameActive(ctx, game, false); err != nil {
		t.Fatal(err)
	}
	refused("inactive game", f.cat, http.StatusNotFound)
	if w := f.get(game01Path, f.gm); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="inactive"`) {
		t.Errorf("the GM of an inactive game: %d", w.Code)
	}
	f.s.SetGameActive(ctx, game, true)

	gm := store.Member{Game: game, Email: acct01.Email}
	if err := f.s.SetManagerActive(ctx, gm, false); err != nil {
		t.Fatal(err)
	}
	refused("inactive GM", f.gm, http.StatusNotFound)
	f.s.SetManagerActive(ctx, gm, true)

	if err := f.s.SetAccountActive(ctx, cat.Email, false); err != nil {
		t.Fatal(err)
	}
	refused("inactive account", f.cat, http.StatusSeeOther)
	f.s.SetAccountActive(ctx, cat.Email, true)
	if w := f.get(folder, f.cat); w.Code != http.StatusOK {
		t.Errorf("after everything is active again: %d", w.Code)
	}
	if all := f.uploads(t, f.game01); len(all) != 1 {
		t.Errorf("a refused upload was stored: %+v", all)
	}
}

// A GM creates, closes, and reopens the turns of their game.
func TestGMManagesTurns(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	page := f.get(game01Path, f.gm).Body.String()
	if !strings.Contains(page, `action="`+game01Path+`/turns"`) || !strings.Contains(page, `name="turn" value="2"`) || !strings.Contains(page, "Create turn 2") {
		t.Fatalf("the GM's game page offers no next turn: %s", page)
	}
	create := func(turn string, cookie *http.Cookie) *httptest.ResponseRecorder {
		return f.post(game01Path+"/turns", url.Values{"turn": {turn}}, cookie, false)
	}
	if w := create("2", f.gm); w.Code != http.StatusSeeOther || w.Header().Get("Location") != game01Path+"/turns/2" {
		t.Fatalf("create turn 2: %d", w.Code)
	}
	// The same form sent twice creates one turn; so does any number but
	// the next.
	for _, turn := range []string{"2", "4", "0"} {
		if w := create(turn, f.gm); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "next turn is 3") {
			t.Errorf("create turn %s: %d %s", turn, w.Code, w.Body)
		}
	}
	for _, turn := range []string{"", "x", "-1", "03"} {
		if w := create(turn, f.gm); w.Code != http.StatusBadRequest {
			t.Errorf("create turn %q: %d", turn, w.Code)
		}
	}
	// Neither a player nor another game's GM has these routes.
	for who, cookie := range map[string]*http.Cookie{"player": f.cat, "another GM": f.gm2} {
		if w := create("3", cookie); w.Code != http.StatusNotFound {
			t.Errorf("%s creates a turn: %d", who, w.Code)
		}
		for _, action := range []string{"close", "open"} {
			if w := f.post(game01Path+"/turns/0/"+action, nil, cookie, false); w.Code != http.StatusNotFound {
				t.Errorf("%s: %s = %d", who, action, w.Code)
			}
		}
	}
	if page := f.get(game01Path, f.cat).Body.String(); strings.Contains(page, "Create turn") || strings.Contains(page, "/players") {
		t.Error("a player is offered the GM's controls")
	}
	if page := f.get(game01Path+"/turns/0", f.cat).Body.String(); strings.Contains(page, "/close") {
		t.Error("a player is offered the close button")
	}
	turns, _ := f.s.Turns(t.Context(), f.game01)
	if len(turns) != 3 || turns[0].Closed() {
		t.Fatalf("turns = %+v", turns)
	}
	if w := f.post(game01Path+"/turns/9/close", nil, f.gm, false); w.Code != http.StatusNotFound {
		t.Errorf("close a missing turn: %d", w.Code)
	}
	f.post(game01Path+"/turns/0/close", nil, f.gm, false)
	if page := f.get(game01Path+"/turns/0", f.gm).Body.String(); !strings.Contains(page, "Reopen turn 0") || !strings.Contains(page, `action="`+game01Path+`/turns/0/open"`) {
		t.Errorf("a closed turn's page: %s", page)
	}
	if page := f.get(game01Path, f.cat).Body.String(); !strings.Contains(page, "closed") {
		t.Error("the game page does not show the closed turn")
	}
}

// A GM gives an email access to the game, and can do nothing else to an
// account.
func TestGMManagesPlayers(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	players := game01Path + "/players"
	page := f.get(players, f.gm).Body.String()
	if !strings.Contains(page, "catbird@example.com") || !strings.Contains(page, "spoonbill@example.com") || strings.Contains(page, "caracara") || strings.Contains(page, "password\"") {
		t.Fatalf("players page: %s", page)
	}
	add := func(email string, cookie *http.Cookie) *httptest.ResponseRecorder {
		return f.post(players, url.Values{"email": {email}}, cookie, false)
	}
	// An unknown email gets an account and one invitation.
	w := add("Bittern@Example.com", f.gm)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), "mailed an invitation") || !strings.Contains(w.Body.String(), "bittern@example.com") {
		t.Fatalf("add a new player: %d %s", w.Code, w.Body)
	}
	msgs := f.mail.Messages()
	if len(msgs) != 1 || msgs[0].To != "bittern@example.com" {
		t.Fatalf("mail = %+v", msgs)
	}
	if link, token := f.lastLink(t); strings.Contains(w.Body.String(), token) || strings.Contains(f.logs.String(), token) || link == "" {
		t.Error("the invitation's link is shown to the GM or logged")
	}
	// An account that exists is added without mail.
	if w := add("caracara@example.com", f.gm); w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), "no invitation") || len(f.mail.Messages()) != 1 {
		t.Errorf("add an existing account: %d %s", w.Code, w.Body)
	}
	if w := add("catbird@example.com", f.gm); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already a player") {
		t.Errorf("add a player twice: %d", w.Code)
	}
	for _, bad := range []string{"", "two words@example.com"} {
		if w := add(bad, f.gm); w.Code != http.StatusBadRequest {
			t.Errorf("add %q: %d", bad, w.Code)
		}
	}
	// A GM never sets a password: a password in the form does nothing.
	f.post(players, url.Values{"email": {"heron@example.com"}, "password": {"a chosen pass phrase"}}, f.gm, false)
	if a, found, _ := f.s.FindAccount(t.Context(), "heron@example.com"); !found || a.HashedSecret != nil {
		t.Error("a GM's form set a password")
	}
	// Disabling a player shuts them out; enabling lets them back.
	set := func(id int, action string, cookie *http.Cookie) *httptest.ResponseRecorder {
		return f.post(fmt.Sprintf("%s/%d/%s", players, id, action), nil, cookie, false)
	}
	if w := set(catNo, "disable", f.gm); w.Code != http.StatusSeeOther || w.Header().Get("Location") != players {
		t.Fatalf("disable: %d", w.Code)
	}
	if w := f.get(game01Path, f.cat); w.Code != http.StatusNotFound {
		t.Errorf("a disabled player reaches the game: %d", w.Code)
	}
	if page := f.get(players, f.gm).Body.String(); !strings.Contains(page, "disabled") || !strings.Contains(page, fmt.Sprintf(`action="%s/%d/enable"`, players, catNo)) {
		t.Errorf("players page after disabling: %s", page)
	}
	set(catNo, "enable", f.gm)
	if w := f.get(game01Path, f.cat); w.Code != http.StatusOK {
		t.Errorf("an enabled player: %d", w.Code)
	}
	// A player of another game is not this game's to change.
	for _, id := range []int{caraNo, 99, 0} {
		if w := set(id, "disable", f.gm); w.Code != http.StatusNotFound {
			t.Errorf("disable player %d: %d", id, w.Code)
		}
	}
	// Players, and other games' GMs, have none of this.
	before := len(f.mail.Messages())
	for who, cookie := range map[string]*http.Cookie{"player": f.spoon, "another GM": f.gm2} {
		if f.get(players, cookie).Code != http.StatusNotFound || add("egret@example.com", cookie).Code != http.StatusNotFound || set(catNo, "disable", cookie).Code != http.StatusNotFound {
			t.Errorf("%s reaches the players routes", who)
		}
	}
	if _, found, _ := f.s.FindAccount(t.Context(), "egret@example.com"); found || len(f.mail.Messages()) != before || f.get(game01Path, f.cat).Code != http.StatusOK {
		t.Error("a refused request changed the players")
	}
}

// The page's posts go through the cross-origin protection.
func TestPagesRefuseCrossOriginPosts(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t, nil)
	folder := folderPath(game01Path, 0, catNo)
	id := f.mustUpload(t, folder, "orders.txt", []byte("orders\n"), f.cat)
	r := uploadRequest(t, folder, "evil.txt", []byte("evil\n"), f.cat)
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := serve(f.h, r); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin upload: %d", w.Code)
	}
	for path, cookie := range map[string]*http.Cookie{
		uploadPath(game01Path, id) + "/withdraw": f.cat, game01Path + "/turns": f.gm, game01Path + "/turns/0/close": f.gm,
		game01Path + "/players": f.gm, fmt.Sprintf("%s/players/%d/disable", game01Path, catNo): f.gm,
	} {
		r := form(path, "", url.Values{"turn": {"2"}, "email": {"new@example.com"}})
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.AddCookie(cookie)
		if w := serve(f.h, r); w.Code != http.StatusForbidden {
			t.Errorf("cross-origin POST %s: %d", path, w.Code)
		}
	}
	turns, _ := f.s.Turns(t.Context(), f.game01)
	if all := f.uploads(t, f.game01); len(all) != 1 || all[0].Withdrawn() || len(turns) != 2 || turns[0].Closed() || len(f.mail.Messages()) != 0 || f.get(game01Path, f.cat).Code != http.StatusOK {
		t.Error("a cross-origin request changed something")
	}
	// The same requests from the page's own origin pass, by Origin alone.
	r = uploadRequest(t, folder, "fine.txt", []byte("fine\n"), f.cat)
	r.Header.Del("Sec-Fetch-Site")
	if w := serve(f.h, r); w.Code != http.StatusCreated {
		t.Errorf("same-origin upload by Origin: %d", w.Code)
	}
}
