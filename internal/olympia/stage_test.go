// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package olympia_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/olympia"
	"github.com/mdhender/fuingo/internal/server"
	"github.com/mdhender/fuingo/internal/store"
)

const pepper = "test-pepper"

var (
	acct01    = devdata.Accounts[0] // manages game01
	acct02    = devdata.Accounts[1]
	catbird   = devdata.Uploaders[0]
	spoonbill = devdata.Uploaders[1]
)

type fixture struct {
	srv   *httptest.Server
	s     *store.Store
	spool string
	batch string
	at    time.Time
	// logins counts calls to Fetch.
	logins int
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
	h, err := server.New(server.Config{Env: "production", Store: s, Pepper: pepper, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	f := &fixture{srv: srv, s: s, spool: filepath.Join(dir, "spool"), batch: filepath.Join(dir, "batch"), at: time.Date(2026, 11, 4, 7, 36, 0, 0, time.UTC)}
	for _, d := range []string{f.spool, f.batch} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// record stores an upload directly, so a test can control the body bytes.
func (f *fixture) record(t *testing.T, u devdata.Uploader, turn, body string) {
	t.Helper()
	gameID, _, err := f.s.GameID(t.Context(), u.Game)
	if err != nil {
		t.Fatal(err)
	}
	up, ok, err := f.s.FindUploader(t.Context(), credential.Hash(pepper, u.Email, gameID, u.Token))
	if err != nil || !ok {
		t.Fatalf("uploader %s: %v %v", u.Email, ok, err)
	}
	sum := sha256.Sum256([]byte(body))
	if err := f.s.RecordUpload(t.Context(), up.ID, up.Email, turn, hex.EncodeToString(sum[:]), []byte(body), f.at); err != nil {
		t.Fatal(err)
	}
	f.at = f.at.Add(time.Minute)
}

// post uploads a turn file through the web form.
func (f *fixture) post(t *testing.T, file string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "turn.txt")
	io.WriteString(fw, file)
	mw.Close()
	resp, err := http.Post(f.srv.URL+"/upload", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %s", resp.Status)
	}
}

func (f *fixture) options(acct devdata.Account, game, turn string) olympia.Options {
	return olympia.Options{
		Game: game, Turn: turn, SpoolDir: f.spool, BatchDir: f.batch,
		Now: func() time.Time { return time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC) },
		Fetch: func(ctx context.Context) (olympia.Fetcher, error) {
			f.logins++
			c, err := olympia.NewClient(f.srv.URL, f.srv.Client())
			if err != nil {
				return nil, err
			}
			return c, c.Login(ctx, acct.Email, acct.Password)
		},
	}
}

// spoolFiles returns the spool's file names and contents.
func (f *fixture) spoolFiles(t *testing.T) ([]string, map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(f.spool)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	contents := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(f.spool, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, e.Name())
		contents[e.Name()] = string(data)
	}
	return names, contents
}

func (f *fixture) clearSpool(t *testing.T) {
	t.Helper()
	names, _ := f.spoolFiles(t)
	for _, n := range names {
		os.Remove(filepath.Join(f.spool, n))
	}
}

// batchEmpty reports whether the batch directory holds nothing but the
// game's empty directory.
func (f *fixture) batchEntries(t *testing.T, game string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.batch, game))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestStage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.record(t, catbird, "3", "begin 1 \"pw\"\r\nunit 2\r\nend") // CRLF, no final newline
	f.record(t, spoonbill, "3", "begin 5 \"pw\"\nend\n")
	f.record(t, catbird, "4", "begin 1 \"pw\"\nnext turn\nend\n") // another turn
	f.record(t, catbird, "3", "begin 1 \"pw\"\nunit 3\n"+strings.Repeat("x", olympia.MaxLineSize+1)+"\nend\n")
	f.post(t, "\xEF\xBB\xBFGAME01 3 CatBird@Example.com "+catbird.Token+"\r\nbegin 1 \"pw\"\r\nend\r\n")

	res, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "3"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Downloaded || f.logins != 1 {
		t.Errorf("downloaded %v, logins %d", res.Downloaded, f.logins)
	}
	names, contents := f.spoolFiles(t)
	want := []string{
		"m3-0001-catbird-example-com-1",
		"m3-0002-spoonbill-example-com-2",
		"m3-0003-catbird-example-com-4",
		"m3-0004-catbird-example-com-5",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("spool = %v, want %v", names, want)
	}
	if got, want := contents[want[0]], "From catbird@example.com Wed Nov  4 07:36:00 2026\nFrom: catbird@example.com\n\nbegin 1 \"pw\"\nunit 2\nend\n"; got != want {
		t.Errorf("first file:\n%q\nwant\n%q", got, want)
	}
	// The uploaded header line and token never reach the spool.
	if got := contents[want[3]]; !strings.HasSuffix(got, "\n\nbegin 1 \"pw\"\nend\n") || strings.Contains(got, "\r") {
		t.Errorf("posted file = %q", got)
	}
	for name, c := range contents {
		if strings.Contains(c, catbird.Token) || strings.Contains(c, acct01.Password) || strings.Contains(strings.ToLower(c), "x-loop") {
			t.Errorf("%s contains a credential or X-Loop: %q", name, c)
		}
		if !strings.HasPrefix(c, "From ") {
			t.Errorf("%s does not start with an mbox From line", name)
		}
	}

	data, err := os.ReadFile(filepath.Join(res.BatchDir, olympia.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{catbird.Token, acct01.Password} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("manifest contains a credential")
		}
	}
	var m olympia.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Game != "game01" || m.Turn != "3" || m.Server != f.srv.URL || len(m.Files) != 4 {
		t.Fatalf("manifest = %+v", m)
	}
	if fl := m.Files[2]; fl.UploadID != 4 || fl.Email != catbird.Email || fl.UploadedAt != "2026-11-04T07:39:00.000000000Z" || !slices.Equal(fl.LongLines, []int{3}) {
		t.Errorf("third entry = %+v", fl)
	}
	if res.BatchDir != filepath.Join(f.batch, "game01", "turn-3") {
		t.Errorf("batch dir = %s", res.BatchDir)
	}
}

func TestStageSharesIdenticalBodies(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.record(t, catbird, "1", "begin\nend\n")
	f.record(t, spoonbill, "1", "begin\nend\n")
	res, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "1"))
	if err != nil {
		t.Fatal(err)
	}
	blobs, _ := os.ReadDir(filepath.Join(res.BatchDir, "blobs"))
	if names, _ := f.spoolFiles(t); len(names) != 2 || len(blobs) != 1 {
		t.Errorf("spool %v, %d blobs", names, len(blobs))
	}
}

func TestStageRefusesOrderFilesInSpool(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.record(t, catbird, "1", "begin\nend\n")
	os.WriteFile(filepath.Join(f.spool, "m-leftover"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(f.spool, "notes"), []byte("ok"), 0o644)
	if _, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "1")); !errors.Is(err, olympia.ErrSpoolNotEmpty) {
		t.Fatalf("err = %v, want ErrSpoolNotEmpty", err)
	}
	if f.logins != 0 || f.batchEntries(t, "game01") != nil {
		t.Errorf("refused stage logged in %d times or made a batch", f.logins)
	}
	opts := f.options(acct01, "game01", "1")
	opts.Force = true
	if _, err := olympia.Stage(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if names, _ := f.spoolFiles(t); !slices.Equal(names, []string{"m-leftover", "m1-0001-catbird-example-com-1", "notes"}) {
		t.Errorf("spool = %v", names)
	}
}

func TestStageReusesFrozenBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.record(t, catbird, "2", "begin\nfirst\nend\n")
	if _, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "2")); err != nil {
		t.Fatal(err)
	}
	_, first := f.spoolFiles(t)
	f.clearSpool(t) // the engine deletes what it reads
	f.record(t, spoonbill, "2", "begin\nlate\nend\n")

	res, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "2"))
	if err != nil {
		t.Fatal(err)
	}
	_, again := f.spoolFiles(t)
	if res.Downloaded || f.logins != 1 || len(again) != 1 || !mapsEqual(first, again) {
		t.Errorf("restage: downloaded %v, logins %d, spool %v", res.Downloaded, f.logins, again)
	}

	f.clearSpool(t)
	opts := f.options(acct01, "game01", "2")
	opts.Rebuild = true
	if res, err = olympia.Stage(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if names, _ := f.spoolFiles(t); !res.Downloaded || len(names) != 2 {
		t.Errorf("rebuild: downloaded %v, spool %v", res.Downloaded, names)
	}
	if entries := f.batchEntries(t, "game01"); !slices.Equal(entries, []string{"turn-2"}) {
		t.Errorf("batch entries after rebuild = %v", entries)
	}
}

func TestStageDetectsDamagedBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.record(t, catbird, "1", "begin\nend\n")
	res, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "1"))
	if err != nil {
		t.Fatal(err)
	}
	f.clearSpool(t)
	blob := filepath.Join(res.BatchDir, "blobs", res.Manifest.Files[0].ContentHash)
	os.WriteFile(blob, []byte("tampered\n"), 0o644)
	if _, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "1")); !errors.Is(err, olympia.ErrBadBatch) {
		t.Errorf("tampered blob: err = %v", err)
	}
	os.Remove(filepath.Join(res.BatchDir, olympia.ManifestName))
	if _, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "1")); !errors.Is(err, olympia.ErrBadBatch) {
		t.Errorf("missing manifest: err = %v", err)
	}
	if names, _ := f.spoolFiles(t); len(names) != 0 {
		t.Errorf("damaged batch staged %v", names)
	}
	opts := f.options(acct01, "game01", "1")
	opts.Rebuild = true
	if _, err := olympia.Stage(t.Context(), opts); err != nil {
		t.Errorf("rebuild: %v", err)
	}
}

func TestStageRefusesUnmanagedGameAndEmptyTurn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.record(t, catbird, "1", "begin\nend\n")
	if _, err := olympia.Stage(t.Context(), f.options(acct02, "game01", "1")); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("unmanaged game: err = %v", err)
	}
	if _, err := olympia.Stage(t.Context(), f.options(acct01, "game01", "2")); !errors.Is(err, olympia.ErrNoUploads) {
		t.Errorf("empty turn: err = %v", err)
	}
	wrong := acct01
	wrong.Password = "wrong"
	if _, err := olympia.Stage(t.Context(), f.options(wrong, "game01", "1")); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong password: err = %v", err)
	}
	if entries := f.batchEntries(t, "game01"); len(entries) != 0 {
		t.Errorf("failed stages left %v", entries)
	}
	if names, _ := f.spoolFiles(t); len(names) != 0 {
		t.Errorf("failed stages wrote %v", names)
	}
}

func TestStageValidatesArguments(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, c := range []struct{ game, turn string }{
		{"GAME01", "1"}, {"../x", "1"}, {".", "1"}, {"", "1"},
		{"game01", ""}, {"game01", "1a"}, {"game01", "../1"}, {"game01", strings.Repeat("1", 33)},
	} {
		if _, err := olympia.Stage(t.Context(), f.options(acct01, c.game, c.turn)); err == nil {
			t.Errorf("game %q turn %q: no error", c.game, c.turn)
		}
	}
	opts := f.options(acct01, "game01", "1")
	opts.SpoolDir = filepath.Join(f.spool, "missing")
	if _, err := olympia.Stage(t.Context(), opts); err == nil {
		t.Error("missing spool: no error")
	}
	if f.logins != 0 {
		t.Errorf("logged in %d times", f.logins)
	}
}

// fakeFetcher serves fixed uploads, with an optional fault.
type fakeFetcher struct {
	uploads []api.Upload
	bodies  map[int64]string
	fault   func(id int64) ([]byte, string, error)
}

func (f *fakeFetcher) Server() string { return "https://fuingo.example" }
func (f *fakeFetcher) Uploads(context.Context, string, string) ([]api.Upload, error) {
	return f.uploads, nil
}
func (f *fakeFetcher) Body(_ context.Context, _ string, id int64) ([]byte, string, error) {
	if f.fault != nil {
		if b, h, err := f.fault(id); b != nil || err != nil {
			return b, h, err
		}
	}
	b := f.bodies[id]
	return []byte(b), hashOf(b), nil
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newFake() *fakeFetcher {
	f := &fakeFetcher{bodies: map[int64]string{1: "begin\none\nend\n", 2: "begin\ntwo\nend\n"}}
	for id := int64(1); id <= 2; id++ {
		f.uploads = append(f.uploads, api.Upload{ID: id, Email: "a@example.com", Turn: "1", ContentHash: hashOf(f.bodies[id]), Size: int64(len(f.bodies[id])), UploadedAt: "2026-11-04T00:00:00.000000000Z"})
	}
	return f
}

func TestStageRejectsBadDownloads(t *testing.T) {
	t.Parallel()
	faults := map[string]func(int64) ([]byte, string, error){
		"interrupted": func(id int64) ([]byte, string, error) {
			if id == 2 {
				return nil, "", io.ErrUnexpectedEOF
			}
			return nil, "", nil
		},
		"wrong body": func(id int64) ([]byte, string, error) {
			if id == 2 {
				return []byte("begin\nother\nend\n"), hashOf("begin\ntwo\nend\n"), nil
			}
			return nil, "", nil
		},
		"truncated body": func(id int64) ([]byte, string, error) {
			if id == 2 {
				return []byte("begin\ntw"), hashOf("begin\ntwo\nend\n"), nil
			}
			return nil, "", nil
		},
		"wrong hash header": func(id int64) ([]byte, string, error) {
			if id == 2 {
				return []byte("begin\ntwo\nend\n"), hashOf("x"), nil
			}
			return nil, "", nil
		},
	}
	for name, fault := range faults {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			spool, batch := filepath.Join(dir, "spool"), filepath.Join(dir, "batch")
			os.Mkdir(spool, 0o755)
			os.Mkdir(batch, 0o755)
			fake := newFake()
			fake.fault = fault
			opts := olympia.Options{Game: "g", Turn: "1", SpoolDir: spool, BatchDir: batch,
				Fetch: func(context.Context) (olympia.Fetcher, error) { return fake, nil }}
			if _, err := olympia.Stage(t.Context(), opts); err == nil {
				t.Fatal("no error")
			}
			if entries, _ := os.ReadDir(filepath.Join(batch, "g")); len(entries) != 0 {
				t.Errorf("batch left %v", entries)
			}
			if entries, _ := os.ReadDir(spool); len(entries) != 0 {
				t.Errorf("spool has %v", entries)
			}
			// A retry with a working server succeeds.
			fake.fault = nil
			if _, err := olympia.Stage(t.Context(), opts); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if entries, _ := os.ReadDir(spool); len(entries) != 2 {
				t.Errorf("retry spool has %v", entries)
			}
		})
	}
}

func TestNewClientRequiresHTTPS(t *testing.T) {
	t.Parallel()
	for url, ok := range map[string]bool{
		"https://fuingo.example":          true,
		"https://htmx-app.localhost:8443": true,
		"http://127.0.0.1:18443":          true,
		"http://localhost:18443":          true,
		"http://[::1]:18443":              true,
		"http://fuingo.example":           false,
		"ftp://fuingo.example":            false,
		"https://user:pw@fuingo.example":  false,
		"fuingo.example":                  false,
		"https://fuingo.example/?token=x": false,
	} {
		if _, err := olympia.NewClient(url, nil); (err == nil) != ok {
			t.Errorf("%s: err = %v", url, err)
		}
	}
}

func TestMessage(t *testing.T) {
	t.Parallel()
	got, err := olympia.Message("a@example.com", "2026-01-02T03:04:05.000000000Z", []byte("\xEF\xBB\xBFbegin\r\nold mac\rend"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "From a@example.com Fri Jan  2 03:04:05 2026\nFrom: a@example.com\n\nbegin\nold mac\nend\n"; string(got) != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
