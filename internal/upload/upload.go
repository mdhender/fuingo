// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package upload implements the turn-file upload handler.
//
// The handler checks each upload in a fixed order: size limit, sanity check,
// authorization, then storage and record in one database transaction. It
// responds with a server-rendered HTML
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
	"log/slog"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	ErrMissingPepper = cerrs.Error("upload pepper is missing or empty")
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
	FieldGame  = "game"
	FieldToken = "token"
	FieldFile  = "file"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Messages shown to the client.
const (
	msgAuthorization = "The email address, game code, or upload token is not valid."
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
	logger *slog.Logger
}

// New returns a handler that authorizes uploads against s using pepper and
// stores their contents in s.
func New(s *store.Store, pepper string, logger *slog.Logger) (*Handler, error) {
	if pepper == "" {
		return nil, ErrMissingPepper
	}
	return &Handler{db: s, pepper: pepper, logger: logger}, nil
}

// form is the parsed request. An absent or oversized email, game, or token is
// left empty, which fails authorization like any other wrong credential.
type form struct {
	email, game, token string
	file               []byte
	hasFile            bool
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
	uploader, ok, err := h.authorize(r, f.email, f.game, f.token)
	if err != nil {
		h.logger.Error("upload: authorize", "err", err)
		respond(w, http.StatusInternalServerError, false, msgServerError)
		return
	} else if !ok {
		respond(w, http.StatusForbidden, false, msgAuthorization)
		return
	}

	// 4 and 5. Storage and record, in one transaction: the contents are kept
	// under their SHA-256 unless already stored, and every upload gets a row.
	sum := sha256.Sum256(f.file)
	contentHash := hex.EncodeToString(sum[:])
	if err := h.db.RecordUpload(r.Context(), uploader.GameID, uploader.Email, contentHash, f.file, time.Now()); err != nil {
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
		case FieldEmail, FieldGame, FieldToken:
			value, err := readPart(part, maxFieldSize)
			if err != nil {
				return f, err
			}
			if len(value) > maxFieldSize {
				value = nil
			}
			switch name {
			case FieldEmail:
				f.email = string(value)
			case FieldGame:
				f.game = string(value)
			case FieldToken:
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

// authorize returns the active uploader whose credential matches. The game
// code is lowercased, like the email. An unknown code still computes and looks up a
// hash, with game ID 0, which no game has, so it fails the same way as a
// wrong token.
func (h *Handler) authorize(r *http.Request, email, game, token string) (store.Uploader, bool, error) {
	if email == "" || game == "" || token == "" {
		return store.Uploader{}, false, nil
	}
	gameID, _, err := h.db.GameID(r.Context(), credential.NormalizeGameCode(game))
	if err != nil {
		return store.Uploader{}, false, err
	}
	hash := credential.Hash(h.pepper, email, gameID, token)
	u, ok, err := h.db.FindUploader(r.Context(), hash)
	if err != nil || !ok {
		return store.Uploader{}, false, err
	}
	if u.GameID != gameID || !credential.Equal(u.CredentialHash, hash) {
		return store.Uploader{}, false, nil
	}
	return u, true, nil
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
