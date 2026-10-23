// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package upload implements the turn-file upload handler.
//
// The handler checks each upload in a fixed order: size limit, sanity check,
// authorization, storage, record. It responds with a server-rendered HTML
// fragment. Every authorization failure gets the same response, so it never
// reveals whether an email exists or which credential was wrong.
package upload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	ErrMissingPepper = cerrs.Error("upload pepper is missing or empty")
	ErrNotDirectory  = cerrs.Error("upload directory is not a directory")
)

const (
	// MaxFileSize is the largest accepted file, in bytes.
	MaxFileSize = 64 << 10
	// maxBodySize bounds the whole request: the file plus the email and token
	// fields and the multipart framing.
	maxBodySize = MaxFileSize + 16<<10
	// maxFieldSize bounds the email and token fields.
	maxFieldSize = 1 << 10
	// sampleSize is how many bytes after an optional BOM the sanity check reads.
	sampleSize = 128
)

// Form field names.
const (
	FieldEmail = "email"
	FieldToken = "token"
	FieldFile  = "file"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Messages shown to the client.
const (
	msgAuthorization = "The email address or upload token is not valid."
	msgBadRequest    = "The upload could not be read. Fill in every field, choose one file, and try again."
	msgEmpty         = "The file is empty."
	msgNoFile        = "Choose a file to upload."
	msgServerError   = "The upload could not be saved. Please try again later."
	msgTooLarge      = "The file is larger than 64 KiB (65,536 bytes)."
)

// Handler accepts turn-file uploads.
type Handler struct {
	db     *store.Store
	pepper string
	dir    string
	logger *slog.Logger
}

// New returns a handler that authorizes uploads against s using pepper and
// stores files in dir, which must already exist.
func New(s *store.Store, pepper, dir string, logger *slog.Logger) (*Handler, error) {
	if pepper == "" {
		return nil, ErrMissingPepper
	}
	sb, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("upload directory: %w", err)
	}
	if !sb.IsDir() {
		return nil, fmt.Errorf("%s: %w", dir, ErrNotDirectory)
	}
	return &Handler{db: s, pepper: pepper, dir: dir, logger: logger}, nil
}

// form is the parsed request. An absent or oversized email or token is left
// empty, which fails authorization like any other wrong credential.
type form struct {
	email, token string
	file         []byte
	hasFile      bool
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Size limit, enforced while reading.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	f, err := readForm(r)
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		respond(w, http.StatusRequestEntityTooLarge, false, msgTooLarge)
		return
	} else if err != nil {
		respond(w, http.StatusBadRequest, false, msgBadRequest)
		return
	}
	if !f.hasFile {
		respond(w, http.StatusBadRequest, false, msgNoFile)
		return
	}
	if len(f.file) > MaxFileSize {
		respond(w, http.StatusRequestEntityTooLarge, false, msgTooLarge)
		return
	}

	// 2. Sanity check.
	if msg, ok := checkText(f.file); !ok {
		respond(w, http.StatusBadRequest, false, msg)
		return
	}

	// 3. Authorization.
	uploader, ok, err := h.authorize(r, f.email, f.token)
	if err != nil {
		h.logger.Error("upload: authorize", "err", err)
		respond(w, http.StatusInternalServerError, false, msgServerError)
		return
	} else if !ok {
		respond(w, http.StatusForbidden, false, msgAuthorization)
		return
	}

	// 4. Storage.
	sum := sha256.Sum256(f.file)
	contentHash := hex.EncodeToString(sum[:])
	if err := h.save(contentHash, f.file); err != nil {
		h.logger.Error("upload: store file", "game_id", uploader.GameID, "content_hash", contentHash, "err", err)
		respond(w, http.StatusInternalServerError, false, msgServerError)
		return
	}

	// 5. Record.
	if err := h.db.RecordUpload(r.Context(), uploader.GameID, uploader.Email, contentHash, time.Now()); err != nil {
		h.logger.Error("upload: record", "game_id", uploader.GameID, "content_hash", contentHash, "err", err)
		respond(w, http.StatusInternalServerError, false, msgServerError)
		return
	}
	h.logger.Info("upload", "game_id", uploader.GameID, "email", uploader.Email, "content_hash", contentHash, "bytes", len(f.file))

	// 6. Response.
	respond(w, http.StatusOK, true, fmt.Sprintf("Upload received: %d bytes, SHA-256 %s.", len(f.file), contentHash))
}

// readForm streams the multipart body, reading at most one byte more than
// each part's limit so oversized parts are detected without buffering them.
func readForm(r *http.Request) (form, error) {
	var f form
	mr, err := r.MultipartReader()
	if err != nil {
		return f, err
	}
	seen := map[string]bool{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return f, nil
		} else if err != nil {
			return f, err
		}
		name := part.FormName()
		if seen[name] {
			return f, fmt.Errorf("duplicate field %q", name)
		}
		seen[name] = true
		switch name {
		case FieldEmail, FieldToken:
			value, err := readPart(part, maxFieldSize)
			if err != nil {
				return f, err
			}
			if len(value) > maxFieldSize {
				value = nil
			}
			if name == FieldEmail {
				f.email = string(value)
			} else {
				f.token = string(value)
			}
		case FieldFile:
			if f.file, err = readPart(part, MaxFileSize); err != nil {
				return f, err
			}
			f.hasFile = part.FileName() != "" || len(f.file) > 0
		default:
			return f, fmt.Errorf("unexpected field %q", name)
		}
	}
}

// readPart reads up to limit+1 bytes of part, then drains the rest so the
// request's size limit still applies to it.
func readPart(part *multipart.Part, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.Discard, part); err != nil {
		return nil, err
	}
	return data, nil
}

// checkText applies the sanity check. It skips one leading UTF-8 BOM, then
// requires every byte of the next sampleSize bytes to be printable ASCII,
// tab, LF, or CR. Later bytes are not checked.
func checkText(data []byte) (string, bool) {
	offset := 0
	if bytes.HasPrefix(data, utf8BOM) {
		offset = len(utf8BOM)
	}
	sample := data[offset:]
	if len(sample) == 0 {
		return msgEmpty, false
	}
	sample = sample[:min(sampleSize, len(sample))]
	for i, b := range sample {
		if !allowed(b) {
			return fmt.Sprintf("The file is not plain text: byte 0x%02X at offset %d is not allowed.", b, offset+i), false
		}
	}
	return "", true
}

func allowed(b byte) bool {
	return (0x20 <= b && b <= 0x7E) || b == '\t' || b == '\n' || b == '\r'
}

// authorize returns the active uploader whose credential matches.
func (h *Handler) authorize(r *http.Request, email, token string) (store.Uploader, bool, error) {
	if email == "" || token == "" {
		return store.Uploader{}, false, nil
	}
	hash := credential.Hash(h.pepper, email, token)
	u, ok, err := h.db.FindUploader(r.Context(), hash)
	if err != nil || !ok {
		return store.Uploader{}, false, err
	}
	if !credential.Equal(u.CredentialHash, hash) {
		return store.Uploader{}, false, nil
	}
	return u, true, nil
}

// save writes data to the upload directory under its content hash. An
// existing file is kept, never rewritten. A new file is written to a
// temporary name and then linked into place, so a partial write is never
// visible under the final name and a concurrent upload of the same content
// cannot replace it.
func (h *Handler) save(contentHash string, data []byte) (err error) {
	final := filepath.Join(h.dir, contentHash)
	if _, err := os.Lstat(final); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(h.dir, ".upload-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, os.Remove(tmp.Name()))
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), final); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

var resultTemplate = template.Must(template.New("result").Parse(
	`<div id="upload-result" role="status" class="{{if .OK}}border-green-300 bg-green-50 text-green-800{{else}}border-red-300 bg-red-50 text-red-800{{end}} rounded-md border p-4 text-sm break-words">{{.Message}}</div>
`))

// respond writes the result fragment.
func respond(w http.ResponseWriter, status int, ok bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = resultTemplate.Execute(w, struct {
		OK      bool
		Message string
	}{ok, message})
}
