// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package upload implements the turn-file upload handler.
//
// Every turn file starts with a header line:
//
//	<game code> <turn number> <email address> <token>
//
// The handler checks each upload in a fixed order: size limit, sanity check,
// header, authorization, then storage of the body and the upload record in one
// database transaction. The header line, and so the token, is never stored.
// The handler responds with a server-rendered HTML fragment. Every
// authorization failure gets the same response, so it never reveals whether an
// email or game exists or which credential was wrong.
//
// Failed authorizations are limited per client address, so repeated guesses
// slow down without locking a valid uploader out from other addresses.
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
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/credential"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	ErrMissingPepper = cerrs.Error("upload pepper is missing or empty")
)

const (
	// MaxFileSize is the largest accepted file, in bytes.
	MaxFileSize = 64 << 10
	// maxBodySize bounds the whole request: the file plus the multipart
	// framing.
	maxBodySize = MaxFileSize + 16<<10
	// sampleSize is how many bytes after an optional BOM the sanity check reads.
	sampleSize = 256
	// maxTurnSize bounds the turn number, which is stored as written.
	maxTurnSize = 32
)

const (
	// FailureBurst is how many failed authorizations a client may make
	// before it is limited.
	FailureBurst = 5
	// FailureInterval is how often a limited client regains one attempt.
	FailureInterval = time.Minute
)

// FieldFile is the form field holding the turn file.
const FieldFile = "file"

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Messages shown to the client.
const (
	msgAuthorization = "The game code, email address, or upload token on the first line is not valid."
	msgBadRequest    = "The upload could not be read. Choose one file and try again."
	msgEmpty         = "The file is empty."
	msgHeader        = "The first line must be the game code, turn number, email address, and upload token, separated by spaces or tabs."
	msgNoBody        = "The file has nothing after the first line."
	msgNoFile        = "Choose a file to upload."
	msgServerError   = "The upload could not be saved. Please try again later."
	msgTooLarge      = "The file is larger than 64 KiB (65,536 bytes)."
	msgTooManyFailed = "Too many failed uploads from your address. Try again in %d seconds."
	msgTurnTooLong   = "The turn number on the first line is longer than 32 characters."
)

// Config configures a Handler.
type Config struct {
	// Store holds the credentials and receives the uploads.
	Store *store.Store
	// Pepper keys the credential hashes. It is required.
	Pepper string
	Logger *slog.Logger
	// Clients finds the client's address behind trusted proxies. The zero
	// Resolver uses the connection's peer.
	Clients clientip.Resolver
	// Failures limits failed authorizations per client. If nil, each client
	// may fail FailureBurst times, then once per FailureInterval.
	Failures *ratelimit.Limiter
}

// Handler accepts turn-file uploads.
type Handler struct {
	db       *store.Store
	pepper   string
	logger   *slog.Logger
	clients  clientip.Resolver
	failures *ratelimit.Limiter
}

// New returns a handler that authorizes uploads against cfg.Store using
// cfg.Pepper and stores their contents in cfg.Store.
func New(cfg Config) (*Handler, error) {
	if cfg.Pepper == "" {
		return nil, ErrMissingPepper
	}
	if cfg.Failures == nil {
		cfg.Failures = ratelimit.New(FailureBurst, FailureInterval, nil)
	}
	return &Handler{db: cfg.Store, pepper: cfg.Pepper, logger: cfg.Logger, clients: cfg.Clients, failures: cfg.Failures}, nil
}

// form is the parsed request.
type form struct {
	file    []byte
	hasFile bool
}

// header is a turn file's first line.
type header struct {
	game, turn, email, token string
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

	// 3. Header.
	hdr, body, msg, ok := parseFile(f.file)
	if !ok {
		respond(w, http.StatusBadRequest, false, msg)
		return
	}

	// 4. Authorization. A failure is reserved before the lookup, so
	// concurrent guesses cannot exceed the client's limit, and given back
	// unless the credentials are wrong.
	client := h.clients.Addr(r)
	if ok, wait := h.failures.Take(client); !ok {
		h.logger.Warn("upload: too many failures", "client", client)
		seconds := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		respond(w, http.StatusTooManyRequests, false, fmt.Sprintf(msgTooManyFailed, seconds))
		return
	}
	uploader, ok, err := h.authorize(r, hdr)
	if err != nil {
		h.failures.Return(client)
		h.logger.Error("upload: authorize", "client", client, "err", err)
		respond(w, http.StatusInternalServerError, false, msgServerError)
		return
	} else if !ok {
		h.logger.Info("upload: authorization failed", "client", client)
		respond(w, http.StatusForbidden, false, msgAuthorization)
		return
	}
	h.failures.Return(client)

	// 5 and 6. Storage of the body and the record, in one transaction: the body
	// is kept under its SHA-256 unless already stored, and every upload gets a
	// row.
	sum := sha256.Sum256(body)
	contentHash := hex.EncodeToString(sum[:])
	if err := h.db.RecordUpload(r.Context(), uploader.GameID, uploader.Email, hdr.turn, contentHash, body, time.Now()); err != nil {
		h.logger.Error("upload: record", "game_id", uploader.GameID, "content_hash", contentHash, "err", err)
		respond(w, http.StatusInternalServerError, false, msgServerError)
		return
	}
	h.logger.Info("upload", "client", client, "game_id", uploader.GameID, "email", uploader.Email, "turn", hdr.turn, "content_hash", contentHash, "bytes", len(body))

	// 7. Response.
	respond(w, http.StatusOK, true, fmt.Sprintf("Received turn %s for %s from %s: %d bytes, SHA-256 %s.",
		hdr.turn, credential.NormalizeGameCode(hdr.game), uploader.Email, len(body), contentHash))
}

// readForm streams the multipart body, reading at most one byte more than
// each part's limit so oversized parts are detected without buffering them.
func readForm(r *http.Request) (form, error) {
	var f form
	mr, err := r.MultipartReader()
	if err != nil {
		return f, err
	}
	seen := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return f, nil
		} else if err != nil {
			return f, err
		}
		if part.FormName() != FieldFile {
			return f, fmt.Errorf("unexpected field %q", part.FormName())
		} else if seen {
			return f, fmt.Errorf("duplicate field %q", FieldFile)
		}
		seen = true
		if f.file, err = readPart(part, MaxFileSize); err != nil {
			return f, err
		}
		f.hasFile = part.FileName() != "" || len(f.file) > 0
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

// parseFile splits a file that passed the sanity check into its header and
// body. It skips one BOM, takes the header line up to the first LF, and trims
// spaces and tabs around it, along with the CR of a CRLF ending. The line must
// be four words of bytes in 0x21-0x7E, separated by spaces or tabs. The body
// is everything after the first LF and must not be empty.
func parseFile(data []byte) (header, []byte, string, bool) {
	data = bytes.TrimPrefix(data, utf8BOM)
	line, body, _ := bytes.Cut(data, []byte{'\n'})
	line = bytes.Trim(bytes.TrimSuffix(line, []byte{'\r'}), " \t")
	words := bytes.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(words) != 4 {
		return header{}, nil, msgHeader, false
	}
	for _, word := range words {
		for _, b := range word {
			if b < 0x21 || b > 0x7E {
				return header{}, nil, msgHeader, false
			}
		}
	}
	hdr := header{game: string(words[0]), turn: string(words[1]), email: string(words[2]), token: string(words[3])}
	if len(hdr.turn) > maxTurnSize {
		return header{}, nil, msgTurnTooLong, false
	}
	if len(body) == 0 {
		return header{}, nil, msgNoBody, false
	}
	return hdr, body, "", true
}

// authorize returns the active uploader whose credential matches the header.
// The game code and email are lowercased. An unknown code still computes and
// looks up a hash, with game ID 0, which no game has, so it fails the same
// way as a wrong token.
func (h *Handler) authorize(r *http.Request, hdr header) (store.Uploader, bool, error) {
	gameID, _, err := h.db.GameID(r.Context(), credential.NormalizeGameCode(hdr.game))
	if err != nil {
		return store.Uploader{}, false, err
	}
	hash := credential.Hash(h.pepper, hdr.email, gameID, hdr.token)
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
