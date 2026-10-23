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

	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/mdhender/fuingo/internal/upload"
)

const pepper = "test-pepper"

var (
	catbird  = devdata.Uploaders[0] // game01
	caracara = devdata.Uploaders[2] // game02
	bom      = "\xEF\xBB\xBF"
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
	h, err := upload.New(s, pepper, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, store: s, handler: h}
}

// request builds a multipart upload like the web form's.
func request(t *testing.T, email, token string, file []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField(upload.FieldEmail, email)
	mw.WriteField(upload.FieldToken, token)
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

func (f *fixture) upload(email, token string, file []byte) response {
	f.t.Helper()
	return f.do(request(f.t, email, token, file))
}

// blobs lists the stored contents.
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

// expectAccepted checks a successful upload: the file is stored under its
// hash and recorded for the uploader.
func (f *fixture) expectAccepted(name string, r response, file []byte) {
	f.t.Helper()
	if r.status != http.StatusOK {
		f.t.Errorf("%s: status %d, want 200: %s", name, r.status, r.body)
		return
	}
	for _, b := range f.blobs() {
		if b.ContentHash == hash(file) {
			if !bytes.Equal(b.Contents, file) {
				f.t.Errorf("%s: stored contents differ from the file", name)
			}
			return
		}
	}
	f.t.Errorf("%s: contents were not stored", name)
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

func text(n int) []byte {
	return bytes.Repeat([]byte("a"), n)
}

func TestSuccessfulUpload(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	file := []byte("turn 1\r\n\tmove north\n")
	before := time.Now().UTC()
	r := f.upload(catbird.Email, catbird.Token, file)
	f.expectAccepted("upload", r, file)
	if !strings.Contains(r.body, hash(file)) {
		t.Errorf("response does not report the content hash: %s", r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	uploads := f.uploads()
	if len(uploads) != 1 {
		t.Fatalf("%d uploads recorded, want 1", len(uploads))
	}
	u := uploads[0]
	if u.GameCode != catbird.Game || u.Email != catbird.Email || u.ContentHash != hash(file) {
		t.Errorf("recorded %+v", u)
	}
	if u.UploadedAt.Location() != time.UTC || u.UploadedAt.Before(before.Add(-time.Second)) || u.UploadedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("uploaded_at = %v", u.UploadedAt)
	}
	if blobs := f.blobs(); len(blobs) != 1 || !blobs[0].CreatedAt.Equal(u.UploadedAt) {
		t.Errorf("blobs = %+v, want one created at %v", blobs, u.UploadedAt)
	}
}

func TestSizeLimit(t *testing.T) {
	t.Parallel()
	t.Run("65536 accepted", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		file := text(65536)
		f.expectAccepted("65536 bytes", f.upload(catbird.Email, catbird.Token, file), file)
	})
	t.Run("65537 rejected", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.expectRejected("65537 bytes", f.upload(catbird.Email, catbird.Token, text(65537)), http.StatusRequestEntityTooLarge, "larger than 64 KiB")
	})
	t.Run("huge body rejected", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.expectRejected("1 MiB", f.upload(catbird.Email, catbird.Token, text(1<<20)), http.StatusRequestEntityTooLarge, "larger than 64 KiB")
	})
	t.Run("size is checked before the sanity check and authorization", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		file := append([]byte{0}, text(65536)...)
		f.expectRejected("bad everything", f.upload("nobody@example.com", "wrong", file), http.StatusRequestEntityTooLarge, "larger than 64 KiB")
	})
}

func TestEmptyFiles(t *testing.T) {
	t.Parallel()
	for name, file := range map[string]string{"empty": "", "BOM only": bom} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.expectRejected(name, f.upload(catbird.Email, catbird.Token, []byte(file)), http.StatusBadRequest, "The file is empty.")
		})
	}
}

func TestMissingFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField(upload.FieldEmail, catbird.Email)
	mw.WriteField(upload.FieldToken, catbird.Token)
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	f.expectRejected("no file", f.do(r), http.StatusBadRequest, "Choose a file")
}

func TestMalformedRequests(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("email=x&token=y"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.expectRejected("urlencoded", f.do(r), http.StatusBadRequest, "could not be read")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField(upload.FieldEmail, catbird.Email)
	mw.WriteField(upload.FieldToken, catbird.Token)
	for range 2 {
		fw, _ := mw.CreateFormFile(upload.FieldFile, "turn.txt")
		fw.Write([]byte("turn"))
	}
	mw.Close()
	r = httptest.NewRequest(http.MethodPost, "/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	f.expectRejected("two files", f.do(r), http.StatusBadRequest, "could not be read")
}

func TestBOMFollowedByText(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	file := []byte(bom + "turn 1\n")
	f.expectAccepted("BOM + text", f.upload(catbird.Email, catbird.Token, file), file)
	// Only one BOM is skipped; a second one is not text.
	f2 := newFixture(t)
	f2.expectRejected("two BOMs", f2.upload(catbird.Email, catbird.Token, []byte(bom+bom+"turn")), http.StatusBadRequest, "byte 0xEF at offset 3")
}

func TestEverySanityByte(t *testing.T) {
	t.Parallel()
	for b := range 256 {
		allowed := (0x20 <= b && b <= 0x7E) || b == 0x09 || b == 0x0A || b == 0x0D
		for _, prefix := range []string{"", bom} {
			f := newFixture(t)
			file := []byte(prefix + string([]byte{byte(b)}) + "turn")
			r := f.upload(catbird.Email, catbird.Token, file)
			name := hex.EncodeToString([]byte{byte(b)})
			if prefix != "" {
				name = "BOM+" + name
			}
			if allowed {
				f.expectAccepted(name, r, file)
			} else {
				f.expectRejected(name, r, http.StatusBadRequest, "not plain text")
			}
		}
	}
}

func TestSampleBoundary(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", bom} {
		t.Run("short files "+hex.EncodeToString([]byte(prefix)), func(t *testing.T) {
			t.Parallel()
			for _, n := range []int{1, 2, 127} {
				f := newFixture(t)
				file := append([]byte(prefix), text(n)...)
				f.expectAccepted("short", f.upload(catbird.Email, catbird.Token, file), file)
			}
			f := newFixture(t)
			f.expectRejected("short with bad last byte", f.upload(catbird.Email, catbird.Token, append([]byte(prefix), 'a', 'b', 0x7F)), http.StatusBadRequest, "not plain text")
		})
		t.Run("last sampled byte "+hex.EncodeToString([]byte(prefix)), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			file := append([]byte(prefix), text(127)...)
			file = append(file, 0x00)
			file = append(file, text(10)...)
			f.expectRejected("byte 128", f.upload(catbird.Email, catbird.Token, file), http.StatusBadRequest, "not plain text")
		})
		t.Run("first byte after the sample "+hex.EncodeToString([]byte(prefix)), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			file := append([]byte(prefix), text(128)...)
			file = append(file, 0x00, 0xFF)
			f.expectAccepted("byte 129", f.upload(catbird.Email, catbird.Token, file), file)
		})
	}
}

func TestSanityIsCheckedBeforeAuthorization(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectRejected("binary with bad token", f.upload(catbird.Email, "wrong", []byte{0x00}), http.StatusBadRequest, "not plain text")
	f.expectRejected("empty with bad token", f.upload(catbird.Email, "wrong", nil), http.StatusBadRequest, "The file is empty.")
}

func TestEmailCaseFolding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	file := []byte("turn\n")
	f.expectAccepted("mixed case", f.upload("CatBird@EXAMPLE.com", catbird.Token, file), file)
	if uploads := f.uploads(); len(uploads) != 1 || uploads[0].Email != catbird.Email {
		t.Errorf("uploads = %+v, want one under %s", uploads, catbird.Email)
	}
}

func TestAuthorizationFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	file := []byte("turn\n")
	cases := map[string][2]string{
		"unknown email":           {"nobody@example.com", catbird.Token},
		"wrong email":             {caracara.Email, catbird.Token},
		"wrong token":             {catbird.Email, "8eb87beb-0869-4815-8ce8-afeef14da57f"},
		"token of another player": {catbird.Email, caracara.Token},
		"empty email":             {"", catbird.Token},
		"empty token":             {catbird.Email, ""},
		"email with whitespace":   {" " + catbird.Email, catbird.Token},
		"oversized email":         {strings.Repeat("a", 2000) + "@example.com", catbird.Token},
	}
	var first *response
	for name, c := range cases {
		r := f.upload(c[0], c[1], file)
		f.expectRejected(name, r, http.StatusForbidden, "The email address or upload token is not valid.")
		if first == nil {
			first = &r
		} else if r.status != first.status || r.body != first.body || !equalHeaders(r.header, first.header) {
			t.Errorf("%s: response differs from the other authorization failures", name)
		}
	}
}

func TestUnknownEmailAndWrongTokenAreIdentical(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	file := []byte("turn\n")
	unknown := f.upload("nobody@example.com", catbird.Token, file)
	wrong := f.upload(catbird.Email, "wrong-token", file)
	if unknown.status != wrong.status || unknown.body != wrong.body || !equalHeaders(unknown.header, wrong.header) {
		t.Errorf("responses differ:\nunknown email: %d %v %q\nwrong token:   %d %v %q",
			unknown.status, unknown.header, unknown.body, wrong.status, wrong.header, wrong.body)
	}
}

func TestInactiveUploaderAndGame(t *testing.T) {
	t.Parallel()
	file := []byte("turn\n")
	t.Run("uploader", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if err := f.store.SetUploaderActive(t.Context(), catbird.Game, catbird.Email, false); err != nil {
			t.Fatal(err)
		}
		f.expectRejected("inactive uploader", f.upload(catbird.Email, catbird.Token, file), http.StatusForbidden, "not valid")
		// Other uploaders in the game are unaffected.
		spoonbill := devdata.Uploaders[1]
		f.expectAccepted("active uploader", f.upload(spoonbill.Email, spoonbill.Token, file), file)
	})
	t.Run("game", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if err := f.store.SetGameActive(t.Context(), catbird.Game, false); err != nil {
			t.Fatal(err)
		}
		f.expectRejected("inactive game", f.upload(catbird.Email, catbird.Token, file), http.StatusForbidden, "not valid")
		f.expectAccepted("active game", f.upload(caracara.Email, caracara.Token, file), file)
	})
}

func TestDifferentPepperRejectsDefaultTokens(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	h, err := upload.New(f.store, "another-pepper", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	f.handler = h
	f.expectRejected("other pepper", f.upload(catbird.Email, catbird.Token, []byte("turn\n")), http.StatusForbidden, "not valid")
}

func TestNewRequiresPepper(t *testing.T) {
	t.Parallel()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := upload.New(s, "", slog.New(slog.DiscardHandler)); !errors.Is(err, upload.ErrMissingPepper) {
		t.Errorf("New without pepper = %v, want ErrMissingPepper", err)
	}
}

func TestDuplicateContent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	file := []byte("setup orders\n")
	f.expectAccepted("first", f.upload(catbird.Email, catbird.Token, file), file)
	first := f.blobs()[0]

	// Another uploader in the same game, the same uploader again, and an
	// uploader in another game all send identical bytes.
	spoonbill := devdata.Uploaders[1]
	f.expectAccepted("another uploader", f.upload(spoonbill.Email, spoonbill.Token, file), file)
	f.expectAccepted("same uploader again", f.upload(catbird.Email, catbird.Token, file), file)
	f.expectAccepted("another game", f.upload(caracara.Email, caracara.Token, file), file)

	// One blob, unchanged since the first upload.
	if blobs := f.blobs(); len(blobs) != 1 || !blobs[0].CreatedAt.Equal(first.CreatedAt) || !bytes.Equal(blobs[0].Contents, first.Contents) {
		t.Errorf("blobs = %+v, want only the first", blobs)
	}
	// One row per upload.
	uploads := f.uploads()
	want := []devdata.Uploader{catbird, spoonbill, catbird, caracara}
	if len(uploads) != len(want) {
		t.Fatalf("%d uploads recorded, want %d", len(uploads), len(want))
	}
	for i, w := range want {
		if uploads[i].GameCode != w.Game || uploads[i].Email != w.Email || uploads[i].ContentHash != hash(file) {
			t.Errorf("upload %d = %+v, want %s %s", i, uploads[i], w.Game, w.Email)
		}
	}
	// The response does not reveal that the content was already stored.
	r1 := f.upload(spoonbill.Email, spoonbill.Token, []byte("new orders\n"))
	r2 := f.upload(spoonbill.Email, spoonbill.Token, []byte("new orders\n"))
	if r1.status != r2.status || r1.body != r2.body {
		t.Errorf("first and repeated uploads differ:\n%q\n%q", r1.body, r2.body)
	}
}

func TestConcurrentDuplicateUploads(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	file := []byte("same turn\n")
	results := make(chan int, 8)
	for range 8 {
		go func() {
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, request(t, catbird.Email, catbird.Token, file))
			results <- w.Code
		}()
	}
	for range 8 {
		if code := <-results; code != http.StatusOK {
			t.Errorf("status %d", code)
		}
	}
	if blobs := f.blobs(); len(blobs) != 1 || blobs[0].ContentHash != hash(file) {
		t.Errorf("blobs = %+v, want one", blobs)
	}
	if n := len(f.uploads()); n != 8 {
		t.Errorf("%d uploads recorded, want 8", n)
	}
}

func equalHeaders(a, b http.Header) bool {
	return maps.EqualFunc(a, b, slices.Equal[[]string])
}
