// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package olympia stages turn files from fuingo as orders for the Olympia G3
// engine.
//
// Staging a game's turn has two steps. First, every upload for the turn is
// downloaded from fuingo's /api/ into a batch directory,
// <batch>/<game>/turn-<turn>/, holding each distinct body under blobs/ by its
// SHA-256 and a manifest.json listing the files to stage. Every body is
// checked against its hash before the batch directory appears, so a batch
// on disk is always complete. Once a batch exists it is frozen: staging the
// turn again reuses it, and later uploads are ignored, unless the caller
// asks to rebuild it.
//
// Second, each manifest entry is written to the engine's spool directory as
// an mbox-style message whose name starts with "m", in upload order:
//
//	From alice@example.com Wed Nov  4 07:36:00 2026
//	From: alice@example.com
//
//	<the upload's body, with LF line endings>
//
// The engine deletes spool files as it reads them, so a turn can be staged
// again from its batch. Staging refuses a spool directory that already holds
// files starting with "m" unless forced.
package olympia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/cerrs"
)

const (
	ErrBadGame       = cerrs.Error("game code must be lowercase letters, digits, '.', '_', or '-', starting with a letter or digit")
	ErrBadTurn       = cerrs.Error("turn must be decimal digits, at most 32 of them")
	ErrBadBatch      = cerrs.Error("the batch is damaged; stage it again with --rebuild")
	ErrBadDownload   = cerrs.Error("a downloaded upload does not match its content hash")
	ErrNoUploads     = cerrs.Error("there are no uploads for the turn")
	ErrSpoolNotEmpty = cerrs.Error("the spool directory already holds order files (names starting with \"m\"); remove them or use --force")
)

const (
	// ManifestName is the name of the manifest in a batch directory.
	ManifestName = "manifest.json"
	// MaxLineSize is the longest line the engine reads without truncating.
	MaxLineSize = 1023
	// spoolTempPrefix marks spool files being written. The engine reads only
	// names starting with "m", so it never reads one.
	spoolTempPrefix = ".olympia-"
)

var (
	gamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	turnPattern = regexp.MustCompile(`^[0-9]{1,32}$`)
	filePattern = regexp.MustCompile(`^m[0-9a-z][0-9a-z._-]*$`)
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	slugRun     = regexp.MustCompile(`[^a-z0-9]+`)
)

// Manifest describes a frozen batch. It never holds credentials.
type Manifest struct {
	Game      string         `json:"game"`
	Turn      string         `json:"turn"`
	Server    string         `json:"server"`
	CreatedAt string         `json:"created_at"`
	Files     []ManifestFile `json:"files"`
}

// ManifestFile is one spool file, in the order the engine applies them.
// LongLines lists the lines of the body, counting from 1, longer than
// MaxLineSize bytes, which the engine truncates.
type ManifestFile struct {
	File        string `json:"file"`
	UploadID    int64  `json:"upload_id"`
	Email       string `json:"email"`
	UploadedAt  string `json:"uploaded_at"`
	ContentHash string `json:"content_hash"`
	Size        int64  `json:"size"`
	LongLines   []int  `json:"long_lines,omitempty"`
}

// Fetcher downloads uploads from fuingo. *Client implements it.
type Fetcher interface {
	Server() string
	Uploads(ctx context.Context, game, turn string) ([]api.Upload, error)
	Body(ctx context.Context, game string, id int64) ([]byte, string, error)
}

// Options configure Stage.
type Options struct {
	Game string
	Turn string
	// SpoolDir is the engine's spool directory, <libdir>/spool.
	SpoolDir string
	// BatchDir holds the batches, one directory per game and turn.
	BatchDir string
	// Rebuild downloads the batch again even if it exists.
	Rebuild bool
	// Force stages even if the spool already holds files starting with "m".
	Force bool
	// Fetch returns the fetcher, logging in if necessary. It is called only
	// when the batch must be downloaded.
	Fetch func(context.Context) (Fetcher, error)
	// Now returns the current time. If nil, it is time.Now.
	Now func() time.Time
}

// Result reports what Stage did.
type Result struct {
	// BatchDir is the turn's batch directory.
	BatchDir string
	// Downloaded is true if the batch was downloaded, false if an existing
	// one was reused.
	Downloaded bool
	Manifest   Manifest
}

// Stage stages the uploads for a game's turn into the spool directory,
// downloading the batch first unless it already exists.
func Stage(ctx context.Context, opts Options) (Result, error) {
	if !gamePattern.MatchString(opts.Game) {
		return Result{}, fmt.Errorf("%q: %w", opts.Game, ErrBadGame)
	}
	if !turnPattern.MatchString(opts.Turn) {
		return Result{}, fmt.Errorf("%q: %w", opts.Turn, ErrBadTurn)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := checkSpool(opts.SpoolDir, opts.Force); err != nil {
		return Result{}, err
	}
	if err := requireDir(opts.BatchDir); err != nil {
		return Result{}, fmt.Errorf("batch directory: %w", err)
	}
	res := Result{BatchDir: filepath.Join(opts.BatchDir, opts.Game, "turn-"+opts.Turn)}

	var m Manifest
	err := fs.ErrNotExist
	if !opts.Rebuild {
		m, err = loadBatch(res.BatchDir, opts.Game, opts.Turn)
	}
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		if m, err = download(ctx, opts, res.BatchDir); err != nil {
			return Result{}, err
		}
		res.Downloaded = true
	default:
		return Result{}, err
	}
	res.Manifest = m
	return res, writeSpool(opts.SpoolDir, res.BatchDir, m)
}

// checkSpool requires an existing spool directory without order files,
// unless forced.
func checkSpool(dir string, force bool) error {
	if err := requireDir(dir); err != nil {
		return fmt.Errorf("spool directory: %w", err)
	}
	if force {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "m") {
			names = append(names, e.Name())
		}
	}
	if len(names) > 0 {
		return fmt.Errorf("%s: %w: %s", dir, ErrSpoolNotEmpty, strings.Join(names, ", "))
	}
	return nil
}

func requireDir(dir string) error {
	if dir == "" {
		return errors.New("not set")
	}
	sb, err := os.Stat(dir)
	if err != nil {
		return err
	} else if !sb.IsDir() {
		return fmt.Errorf("%s: not a directory", dir)
	}
	return nil
}

// loadBatch reads a batch's manifest and checks every blob it names against
// its hash. It returns an error wrapping fs.ErrNotExist if there is no batch.
func loadBatch(dir, game, turn string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		if _, statErr := os.Stat(dir); statErr == nil {
			return m, fmt.Errorf("%s: %w: no %s", dir, ErrBadBatch, ManifestName)
		}
		return m, err
	} else if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("%s: %w: %v", dir, ErrBadBatch, err)
	}
	if m.Game != game || m.Turn != turn || len(m.Files) == 0 {
		return m, fmt.Errorf("%s: %w: the manifest is for game %q turn %q with %d files", dir, ErrBadBatch, m.Game, m.Turn, len(m.Files))
	}
	for _, f := range m.Files {
		if !filePattern.MatchString(f.File) || !hashPattern.MatchString(f.ContentHash) {
			return m, fmt.Errorf("%s: %w: bad entry %q", dir, ErrBadBatch, f.File)
		}
		if _, err := readBlob(dir, f.ContentHash); err != nil {
			return m, err
		}
	}
	return m, nil
}

// readBlob reads a blob and checks it against its hash.
func readBlob(batch, hash string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(batch, "blobs", hash))
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %v", batch, ErrBadBatch, err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("%s: %w: blob %s does not match its hash", batch, ErrBadBatch, hash)
	}
	return data, nil
}

// download fetches the turn's uploads into a temporary directory beside the
// batch and, once every body is verified and the manifest written, renames
// it into place, replacing any earlier batch.
func download(ctx context.Context, opts Options, batch string) (_ Manifest, err error) {
	f, err := opts.Fetch(ctx)
	if err != nil {
		return Manifest{}, err
	}
	uploads, err := f.Uploads(ctx, opts.Game, opts.Turn)
	if err != nil {
		return Manifest{}, err
	}
	if len(uploads) == 0 {
		return Manifest{}, fmt.Errorf("game %s turn %s: %w", opts.Game, opts.Turn, ErrNoUploads)
	}

	parent := filepath.Dir(batch)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Manifest{}, err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(batch)+".tmp-")
	if err != nil {
		return Manifest{}, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := os.Mkdir(filepath.Join(tmp, "blobs"), 0o755); err != nil {
		return Manifest{}, err
	}

	m := Manifest{Game: opts.Game, Turn: opts.Turn, Server: f.Server(), CreatedAt: opts.Now().UTC().Format(api.TimeLayout)}
	for i, u := range uploads {
		if u.Turn != opts.Turn {
			return Manifest{}, fmt.Errorf("upload %d is for turn %q, not %q", u.ID, u.Turn, opts.Turn)
		}
		if !hashPattern.MatchString(u.ContentHash) {
			return Manifest{}, fmt.Errorf("upload %d: %w: bad hash %q", u.ID, ErrBadDownload, u.ContentHash)
		}
		body, err := downloadBlob(ctx, f, opts.Game, tmp, u)
		if err != nil {
			return Manifest{}, err
		}
		m.Files = append(m.Files, ManifestFile{
			File:        spoolName(opts.Turn, i+1, u.Email, u.ID),
			UploadID:    u.ID,
			Email:       u.Email,
			UploadedAt:  u.UploadedAt,
			ContentHash: u.ContentHash,
			Size:        u.Size,
			LongLines:   longLines(normalize(body)),
		})
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, ManifestName), append(data, '\n'), 0o644); err != nil {
		return Manifest{}, err
	}

	// Replace any earlier batch. The old one is moved aside first, so the
	// batch directory is always either the old batch or the new one.
	var old string
	if _, err := os.Stat(batch); err == nil {
		old = tmp + ".old"
		if err := os.Rename(batch, old); err != nil {
			return Manifest{}, err
		}
	}
	if err := os.Rename(tmp, batch); err != nil {
		if old != "" {
			_ = os.Rename(old, batch)
		}
		return Manifest{}, err
	}
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return m, nil
}

// downloadBlob downloads an upload's body into dir/blobs, unless an
// identical body is already there, and checks it against the listed hash,
// size, and the hash the server sent with it.
func downloadBlob(ctx context.Context, f Fetcher, game, dir string, u api.Upload) ([]byte, error) {
	path := filepath.Join(dir, "blobs", u.ContentHash)
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}
	body, sent, err := f.Body(ctx, game, u.ID)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != u.ContentHash || sent != u.ContentHash || int64(len(body)) != u.Size {
		return nil, fmt.Errorf("upload %d: %w", u.ID, ErrBadDownload)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return nil, err
	}
	return body, nil
}

// spoolName names the seq'th spool file of a turn. Names sort in upload
// order and always start with "m".
func spoolName(turn string, seq int, email string, uploadID int64) string {
	return fmt.Sprintf("m%s-%04d-%s-%d", turn, seq, slug(email), uploadID)
}

// slug lowercases s and replaces each run of other bytes than letters and
// digits with one '-'.
func slug(s string) string {
	s = strings.Trim(slugRun.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "x"
	}
	return s
}

// writeSpool writes every file in the manifest to the spool directory. All
// of them are first written under names the engine ignores, then renamed, so
// a failure part way leaves no order files.
func writeSpool(spool, batch string, m Manifest) (err error) {
	var temps []string
	defer func() {
		for _, tmp := range temps {
			_ = os.Remove(tmp) // already renamed unless err != nil
		}
	}()
	for _, f := range m.Files {
		body, err := readBlob(batch, f.ContentHash)
		if err != nil {
			return err
		}
		msg, err := Message(f.Email, f.UploadedAt, body)
		if err != nil {
			return fmt.Errorf("%s: %w", f.File, err)
		}
		tmp := filepath.Join(spool, spoolTempPrefix+f.File)
		temps = append(temps, tmp)
		if err := os.WriteFile(tmp, msg, 0o644); err != nil {
			return err
		}
	}
	for i, f := range m.Files {
		if err := os.Rename(temps[i], filepath.Join(spool, f.File)); err != nil {
			return err
		}
	}
	return nil
}

// Message returns the spool file for an upload: an mbox "From " line with
// the email and the upload time in asctime form, a From: header, an empty
// line, and the body with LF line endings, ending with a newline.
func Message(email, uploadedAt string, body []byte) ([]byte, error) {
	at, err := time.Parse(api.TimeLayout, uploadedAt)
	if err != nil {
		return nil, fmt.Errorf("upload time: %w", err)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From %s %s\n", email, at.UTC().Format(time.ANSIC))
	fmt.Fprintf(&b, "From: %s\n\n", email)
	b.Write(normalize(body))
	return b.Bytes(), nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// normalize strips a leading BOM, converts CRLF and bare CR line endings to
// LF, and ends the body with a newline.
func normalize(body []byte) []byte {
	body = bytes.TrimPrefix(body, utf8BOM)
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	body = bytes.ReplaceAll(body, []byte("\r"), []byte("\n"))
	if len(body) > 0 && body[len(body)-1] != '\n' {
		body = append(body, '\n')
	}
	return body
}

// longLines returns the numbers of the lines longer than MaxLineSize.
func longLines(body []byte) []int {
	var long []int
	for i, line := range bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n")) {
		if len(line) > MaxLineSize {
			long = append(long, i+1)
		}
	}
	return long
}

// String describes a manifest file for the command's output.
func (f ManifestFile) String() string {
	s := f.File + " (" + f.Email + ", upload " + strconv.FormatInt(f.UploadID, 10) + ")"
	if len(f.LongLines) > 0 {
		s += fmt.Sprintf(": warning: lines %v are longer than %d bytes and will be truncated", f.LongLines, MaxLineSize)
	}
	return s
}
