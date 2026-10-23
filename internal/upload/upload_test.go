// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package upload_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/mdhender/fuingo/internal/upload"
)

const pepper = "test-pepper"

var (
	catbird   = devdata.Uploaders[0] // game01
	spoonbill = devdata.Uploaders[1] // game01
	caracara  = devdata.Uploaders[2] // game02
	bom       = "\xEF\xBB\xBF"
)

const (
	msgAuthorization = "The game code, email address, or upload token on the first line is not valid."
	msgHeader        = "The first line must be the game code, turn number, email address, and upload token"
	msgEmpty         = "The file is empty."
	msgNoBody        = "The file has nothing after the first line."
	msgNotText       = "not plain text"
	msgTooLarge      = "larger than 64 KiB"
	msgTurnTooLong   = "longer than 32 characters"
)

type fixture struct {
	t       *testing.T
	store   *store.Store
	handler http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := devdata.Seed(t.Context(), s, "development", pepper); err != nil {
		t.Fatal(err)
	}
	// Most tests fail authorization many times from one address; the limit
	// has its own tests.
	h, err := upload.New(upload.Config{Store: s, Pepper: pepper, Logger: slog.New(slog.DiscardHandler), Failures: ratelimit.New(1000, time.Minute, nil)})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, store: s, handler: h}
}

// headerLine returns u's header line for turn 1, with an LF.
func headerLine(u devdata.Uploader) string {
	return u.Game + " 1 " + u.Email + " " + u.Token + "\n"
}

// turnFile returns a turn file for u: the header line, then body.
func turnFile(u devdata.Uploader, body string) []byte {
	return []byte(headerLine(u) + body)
}

// request builds a multipart upload like the web form's.
func request(t *testing.T, file []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile(upload.FieldFile, "turn.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(file)
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

type response struct {
	status int
	body   string
	header http.Header
}

func (f *fixture) do(r *http.Request) response {
	f.t.Helper()
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return response{status: w.Code, body: w.Body.String(), header: w.Header()}
}

func (f *fixture) upload(file []byte) response {
	f.t.Helper()
	return f.do(request(f.t, file))
}

func (f *fixture) blobs() []store.Blob {
	f.t.Helper()
	blobs, err := f.store.Blobs(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	return blobs
}

func (f *fixture) uploads() []store.Upload {
	f.t.Helper()
	uploads, err := f.store.Uploads(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	return uploads
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// expectAccepted checks a successful upload: the body (and only the body) is
// stored under its hash, and the response gives that hash but not the token.
func (f *fixture) expectAccepted(name string, r response, body string) {
	f.t.Helper()
	if r.status != http.StatusOK {
		f.t.Errorf("%s: status %d, want 200: %s", name, r.status, r.body)
		return
	}
	if !strings.Contains(r.body, hash([]byte(body))) {
		f.t.Errorf("%s: response does not give the body's hash: %s", name, r.body)
	}
	for _, u := range devdata.Uploaders {
		if strings.Contains(r.body, u.Token) {
			f.t.Errorf("%s: response contains a token: %s", name, r.body)
		}
	}
	for _, b := range f.blobs() {
		if b.ContentHash == hash([]byte(body)) {
			if string(b.Contents) != body {
				f.t.Errorf("%s: stored %q, want %q", name, b.Contents, body)
			}
			return
		}
	}
	f.t.Errorf("%s: body %q was not stored", name, body)
}

// expectRejected checks a failed upload: nothing stored or recorded.
func (f *fixture) expectRejected(name string, r response, status int, message string) {
	f.t.Helper()
	if r.status != status {
		f.t.Errorf("%s: status %d, want %d: %s", name, r.status, status, r.body)
	}
	if !strings.Contains(r.body, message) {
		f.t.Errorf("%s: body %q does not contain %q", name, r.body, message)
	}
	if blobs := f.blobs(); len(blobs) != 0 {
		f.t.Errorf("%s: %d blobs stored", name, len(blobs))
	}
	if uploads := f.uploads(); len(uploads) != 0 {
		f.t.Errorf("%s: %d uploads recorded", name, len(uploads))
	}
}

func TestSuccessfulUpload(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	body := "move north\r\n\tattack\n"
	r := f.upload([]byte("game01 12 " + catbird.Email + " " + catbird.Token + "\n" + body))
	f.expectAccepted("upload", r, body)
	for _, want := range []string{"turn 12", "game01", catbird.Email} {
		if !strings.Contains(r.body, want) {
			t.Errorf("response does not mention %q: %s", want, r.body)
		}
	}
	if ct := r.header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	uploads := f.uploads()
	if len(uploads) != 1 {
		t.Fatalf("%d uploads recorded, want 1", len(uploads))
	}
	u := uploads[0]
	if u.GameCode != "game01" || u.Email != catbird.Email || u.TurnNo != "12" || u.ContentHash != hash([]byte(body)) {
		t.Errorf("recorded %+v", u)
	}
	if blobs := f.blobs(); len(blobs) != 1 || !blobs[0].CreatedAt.Equal(u.UploadedAt) {
		t.Errorf("blobs = %+v, want one created at %v", blobs, u.UploadedAt)
	}
}

func TestSizeLimit(t *testing.T) {
	t.Parallel()
	// sized returns a valid turn file of exactly n bytes.
	sized := func(n int) []byte {
		file := turnFile(catbird, "")
		return append(file, bytes.Repeat([]byte("a"), n-len(file))...)
	}
	t.Run("65536 accepted", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		file := sized(65536)
		f.expectAccepted("65536 bytes", f.upload(file), string(file[len(headerLine(catbird)):]))
	})
	t.Run("65537 rejected", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.expectRejected("65537 bytes", f.upload(sized(65537)), http.StatusRequestEntityTooLarge, msgTooLarge)
	})
	t.Run("huge body rejected", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.expectRejected("1 MiB", f.upload(sized(1<<20)), http.StatusRequestEntityTooLarge, msgTooLarge)
	})
	t.Run("size is checked first", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		file := append([]byte{0}, bytes.Repeat([]byte("a"), 65536)...)
		f.expectRejected("bad everything", f.upload(file), http.StatusRequestEntityTooLarge, msgTooLarge)
	})
}

func TestEmptyFiles(t *testing.T) {
	t.Parallel()
	for name, file := range map[string]string{"empty": "", "BOM only": bom} {
		f := newFixture(t)
		f.expectRejected(name, f.upload([]byte(file)), http.StatusBadRequest, msgEmpty)
	}
}

func TestMissingFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	f.expectRejected("no file", f.do(r), http.StatusBadRequest, "Choose a file")
}

func TestMalformedRequests(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("file=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.expectRejected("urlencoded", f.do(r), http.StatusBadRequest, "could not be read")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for range 2 {
		fw, _ := mw.CreateFormFile(upload.FieldFile, "turn.txt")
		fw.Write(turnFile(catbird, "orders\n"))
	}
	mw.Close()
	r = httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	f.expectRejected("two files", f.do(r), http.StatusBadRequest, "could not be read")

	body.Reset()
	mw = multipart.NewWriter(&body)
	mw.WriteField("email", catbird.Email)
	fw, _ := mw.CreateFormFile(upload.FieldFile, "turn.txt")
	fw.Write(turnFile(catbird, "orders\n"))
	mw.Close()
	r = httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	f.expectRejected("extra field", f.do(r), http.StatusBadRequest, "could not be read")
}

func TestBOM(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectAccepted("BOM + file", f.upload([]byte(bom+headerLine(catbird)+"orders\n")), "orders\n")
	// The stored body never has the BOM.
	for _, b := range f.blobs() {
		if bytes.Contains(b.Contents, []byte(bom)) {
			t.Errorf("stored body contains a BOM")
		}
	}
	// Only one BOM is skipped; a second one is not text.
	f2 := newFixture(t)
	f2.expectRejected("two BOMs", f2.upload([]byte(bom+bom+headerLine(catbird)+"orders\n")), http.StatusBadRequest, "byte 0xEF at offset 3")
}

func TestEverySanityByte(t *testing.T) {
	t.Parallel()
	// The byte under test is the first byte of the body, inside the sample.
	for b := range 256 {
		allowed := (0x20 <= b && b <= 0x7E) || b == 0x09 || b == 0x0A || b == 0x0D
		for _, prefix := range []string{"", bom} {
			f := newFixture(t)
			body := string([]byte{byte(b)}) + "orders\n"
			r := f.upload([]byte(prefix + headerLine(catbird) + body))
			name := hex.EncodeToString([]byte{byte(b)})
			if prefix != "" {
				name = "BOM+" + name
			}
			if allowed {
				f.expectAccepted(name, r, body)
			} else {
				f.expectRejected(name, r, http.StatusBadRequest, msgNotText)
			}
		}
	}
}

func TestSampleBoundary(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", bom} {
		name := "no BOM"
		if prefix != "" {
			name = "BOM"
		}
		// padded returns a file whose bytes after the BOM are the header line,
		// then "a"s up to offset at, then extra.
		padded := func(at int, extra []byte) ([]byte, string) {
			hdr := headerLine(catbird)
			body := string(bytes.Repeat([]byte("a"), at-len(hdr))) + string(extra)
			return []byte(prefix + hdr + body), body
		}
		t.Run(name+": shorter than the sample", func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			file, body := padded(len(headerLine(catbird))+1, nil)
			f.expectAccepted("short", f.upload(file), body)
			f = newFixture(t)
			file, body = padded(255, []byte("\n"))
			f.expectAccepted("255 bytes then LF", f.upload(file), body)
		})
		t.Run(name+": last sampled byte", func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			file, _ := padded(255, []byte{0x00, 'a'})
			f.expectRejected("byte 256", f.upload(file), http.StatusBadRequest, msgNotText)
		})
		t.Run(name+": first byte after the sample", func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			file, body := padded(256, []byte{0x00, 0xFF})
			f.expectAccepted("byte 257", f.upload(file), body)
		})
	}
}

func TestHeaderFormats(t *testing.T) {
	t.Parallel()
	creds := catbird.Email + " " + catbird.Token
	accepted := map[string]string{
		"tabs":                           "game01\t1\t" + catbird.Email + "\t" + catbird.Token + "\norders\n",
		"multiple spaces and tabs":       "game01  \t 1 \t\t" + catbird.Email + "   " + catbird.Token + "\norders\n",
		"leading and trailing blanks":    " \t game01 1 " + creds + " \t \norders\n",
		"CRLF":                           "game01 1 " + creds + "\r\norders\r\n",
		"CRLF after trailing blanks":     "game01 1 " + creds + " \t\r\norders\r\n",
		"32-byte turn number":            "game01 " + strings.Repeat("9", 32) + " " + creds + "\norders\n",
		"punctuation in the turn":        "game01 setup-1.b " + creds + "\norders\n",
		"body that is only a blank line": "game01 1 " + creds + "\n\n",
	}
	for name, file := range accepted {
		f := newFixture(t)
		_, body, _ := strings.Cut(file, "\n")
		f.expectAccepted(name, f.upload([]byte(file)), body)
	}

	rejected := map[string]struct{ file, message string }{
		"three words":          {"game01 1 " + catbird.Email + "\norders\n", msgHeader},
		"five words":           {"game01 1 " + creds + " extra\norders\n", msgHeader},
		"blank first line":     {"\ngame01 1 " + creds + "\norders\n", msgHeader},
		"33-byte turn number":  {"game01 " + strings.Repeat("9", 33) + " " + creds + "\norders\n", msgTurnTooLong},
		"bare CR":              {"game01 1 " + creds + "\rorders\r", msgHeader},
		"CR inside the line":   {"game01 1\r" + creds + "\norders\n", msgHeader},
		"no LF":                {"game01 1 " + creds, msgNoBody},
		"header and LF only":   {"game01 1 " + creds + "\n", msgNoBody},
		"header and CRLF only": {"game01 1 " + creds + "\r\n", msgNoBody},
		// A header longer than the sample is still checked byte by byte.
		"control byte beyond the sample":   {"game01 1 " + creds + strings.Repeat("x", 300) + "\x01\norders\n", msgHeader},
		"non-ASCII byte beyond the sample": {"game01 1 " + creds + strings.Repeat("x", 300) + "\xC3\xA9\norders\n", msgHeader},
	}
	for name, c := range rejected {
		f := newFixture(t)
		f.expectRejected(name, f.upload([]byte(c.file)), http.StatusBadRequest, c.message)
	}
}

func TestFormatIsCheckedBeforeAuthorization(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectRejected("binary body with a bad token", f.upload([]byte("game01 1 "+catbird.Email+" wrong\n\x00")), http.StatusBadRequest, msgNotText)
	f.expectRejected("bad header with a bad token", f.upload([]byte("game01 1 wrong\norders\n")), http.StatusBadRequest, msgHeader)
	f.expectRejected("empty body with a bad token", f.upload([]byte("game01 1 "+catbird.Email+" wrong\n")), http.StatusBadRequest, msgNoBody)
}

func TestCaseFolding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectAccepted("mixed case", f.upload([]byte("GaMe01 1 CatBird@EXAMPLE.com "+catbird.Token+"\norders\n")), "orders\n")
	if uploads := f.uploads(); len(uploads) != 1 || uploads[0].Email != catbird.Email || uploads[0].GameCode != catbird.Game {
		t.Errorf("uploads = %+v, want one for %s in %s", uploads, catbird.Email, catbird.Game)
	}
	// The token is matched exactly.
	f2 := newFixture(t)
	f2.expectRejected("token case", f2.upload([]byte("game01 1 "+catbird.Email+" "+strings.ToUpper(catbird.Token)+"\norders\n")), http.StatusForbidden, msgAuthorization)
}

func TestAuthorizationFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	line := func(game, email, token string) []byte {
		return []byte(game + " 1 " + email + " " + token + "\norders\n")
	}
	cases := map[string][]byte{
		"unknown email":               line(catbird.Game, "nobody@example.com", catbird.Token),
		"wrong email":                 line(catbird.Game, spoonbill.Email, catbird.Token),
		"wrong token":                 line(catbird.Game, catbird.Email, "8eb87beb-0869-4815-8ce8-afeef14da57f"),
		"token of another player":     line(catbird.Game, catbird.Email, spoonbill.Token),
		"unknown game":                line("game99", catbird.Email, catbird.Token),
		"game the uploader is not in": line(caracara.Game, catbird.Email, catbird.Token),
		"game of another player":      line(catbird.Game, caracara.Email, caracara.Token),
	}
	var first *response
	for name, file := range cases {
		r := f.upload(file)
		f.expectRejected(name, r, http.StatusForbidden, msgAuthorization)
		if first == nil {
			first = &r
		} else if r.status != first.status || r.body != first.body || !equalHeaders(r.header, first.header) {
			t.Errorf("%s: response differs from the other authorization failures", name)
		}
	}
}

func TestInactiveUploaderAndGame(t *testing.T) {
	t.Parallel()
	t.Run("uploader", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if err := f.store.SetUploaderActive(t.Context(), catbird.Game, catbird.Email, false); err != nil {
			t.Fatal(err)
		}
		f.expectRejected("inactive uploader", f.upload(turnFile(catbird, "orders\n")), http.StatusForbidden, msgAuthorization)
		f.expectAccepted("active uploader", f.upload(turnFile(spoonbill, "orders\n")), "orders\n")
	})
	t.Run("game", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if err := f.store.SetGameActive(t.Context(), catbird.Game, false); err != nil {
			t.Fatal(err)
		}
		f.expectRejected("inactive game", f.upload(turnFile(catbird, "orders\n")), http.StatusForbidden, msgAuthorization)
		f.expectAccepted("active game", f.upload(turnFile(caracara, "orders\n")), "orders\n")
	})
}

func TestSameEmailAndTokenInTwoGames(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	err := f.store.CreateUploader(t.Context(), store.NewUploader{
		GameCode: caracara.Game,
		Email:    catbird.Email,
		CredentialHash: func(gameID int64) []byte {
			return credential.Hash(pepper, catbird.Email, gameID, catbird.Token)
		},
	})
	if err != nil {
		t.Fatalf("CreateUploader in a second game: %v", err)
	}
	f.expectAccepted("game01", f.upload([]byte("game01 1 "+catbird.Email+" "+catbird.Token+"\ngame01 orders\n")), "game01 orders\n")
	f.expectAccepted("game02", f.upload([]byte("game02 1 "+catbird.Email+" "+catbird.Token+"\ngame02 orders\n")), "game02 orders\n")
	uploads := f.uploads()
	if len(uploads) != 2 || uploads[0].GameCode != "game01" || uploads[1].GameCode != "game02" {
		t.Errorf("uploads = %+v, want one in each game", uploads)
	}
}

func TestDifferentPepperRejectsDefaultTokens(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	h, err := upload.New(upload.Config{Store: f.store, Pepper: "another-pepper", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = h
	f.expectRejected("other pepper", f.upload(turnFile(catbird, "orders\n")), http.StatusForbidden, msgAuthorization)
}

func TestNewRequiresPepper(t *testing.T) {
	t.Parallel()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := upload.New(upload.Config{Store: s, Logger: slog.New(slog.DiscardHandler)}); !errors.Is(err, upload.ErrMissingPepper) {
		t.Errorf("New without pepper = %v, want ErrMissingPepper", err)
	}
}

func TestDuplicateContent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	body := "setup orders\n"
	f.expectAccepted("first", f.upload(turnFile(catbird, body)), body)
	first := f.blobs()[0]

	// Different header lines, identical bodies: another uploader in the same
	// game, the same uploader again, and an uploader in another game.
	f.expectAccepted("another uploader", f.upload(turnFile(spoonbill, body)), body)
	f.expectAccepted("same uploader again", f.upload(turnFile(catbird, body)), body)
	f.expectAccepted("another game", f.upload(turnFile(caracara, body)), body)

	if blobs := f.blobs(); len(blobs) != 1 || !blobs[0].CreatedAt.Equal(first.CreatedAt) || !bytes.Equal(blobs[0].Contents, first.Contents) {
		t.Errorf("blobs = %+v, want only the first", blobs)
	}
	uploads := f.uploads()
	want := []devdata.Uploader{catbird, spoonbill, catbird, caracara}
	if len(uploads) != len(want) {
		t.Fatalf("%d uploads recorded, want %d", len(uploads), len(want))
	}
	for i, w := range want {
		if uploads[i].GameCode != w.Game || uploads[i].Email != w.Email || uploads[i].ContentHash != hash([]byte(body)) {
			t.Errorf("upload %d = %+v, want %s %s", i, uploads[i], w.Game, w.Email)
		}
	}
	// The response does not reveal that the content was already stored.
	r1 := f.upload(turnFile(spoonbill, "new orders\n"))
	r2 := f.upload(turnFile(spoonbill, "new orders\n"))
	if r1.status != r2.status || r1.body != r2.body {
		t.Errorf("first and repeated uploads differ:\n%q\n%q", r1.body, r2.body)
	}
}

func TestConcurrentDuplicateUploads(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	body := "same turn\n"
	results := make(chan int, 9)
	for i := range 9 {
		go func() {
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, request(t, turnFile(devdata.Uploaders[i%3], body)))
			results <- w.Code
		}()
	}
	for range 9 {
		if code := <-results; code != http.StatusOK {
			t.Errorf("status %d", code)
		}
	}
	if blobs := f.blobs(); len(blobs) != 1 || blobs[0].ContentHash != hash([]byte(body)) {
		t.Errorf("blobs = %+v, want one", blobs)
	}
	if n := len(f.uploads()); n != 9 {
		t.Errorf("%d uploads recorded, want 9", n)
	}
}

func equalHeaders(a, b http.Header) bool {
	return maps.EqualFunc(a, b, slices.Equal[[]string])
}
