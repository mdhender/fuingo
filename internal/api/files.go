// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/store"
)

// DefaultMaxUpload is the most bytes a file uploaded through /api/ may
// hold, unless configured.
const DefaultMaxUpload = 1 << 20

// File describes one uploaded file. Turn is null for a file at the game's
// level, and Player is null unless the file is in a player's folder.
// WithdrawnAt is null unless the file was withdrawn.
type File struct {
	ID          int64   `json:"id"`
	System      string  `json:"system"`
	Game        string  `json:"game"`
	Turn        *int64  `json:"turn"`
	Player      *int64  `json:"player"`
	Name        string  `json:"name"`
	Size        int64   `json:"size"`
	SHA256      string  `json:"sha256"`
	UploadedBy  string  `json:"uploaded_by"`
	UploadedAt  string  `json:"uploaded_at"`
	WithdrawnAt *string `json:"withdrawn_at"`
}

// FileList is the response to GET .../files: the files in one folder.
type FileList struct {
	System string `json:"system"`
	Game   string `json:"game"`
	Turn   *int64 `json:"turn"`
	Player *int64 `json:"player"`
	Files  []File `json:"files"`
}

// UploadList is the response to GET .../uploads: files at every level of a
// game.
type UploadList struct {
	System  string `json:"system"`
	Game    string `json:"game"`
	Uploads []File `json:"uploads"`
}

// Messages in the file routes' error responses.
const (
	msgBadAfter       = "after must be an integer, 0 or more"
	msgBadWithdrawn   = "withdrawn must be include, or left out"
	msgBadFileType    = "the request body must be the file itself, as text/plain or application/octet-stream"
	msgFileTooLarge   = "the file is larger than the limit, in bytes, of "
	msgNotYourUpload  = "only the account that uploaded a file may withdraw it"
	msgFileNameSuffix = "; give it in the name query parameter"
)

func newFile(game store.GameRef, u store.Upload) File {
	f := File{
		ID: u.ID, System: game.System, Game: game.Code, Turn: u.Turn, Player: u.Player,
		Name: u.Name, Size: u.Size, SHA256: u.SHA256,
		UploadedBy: u.UploaderEmail, UploadedAt: u.UploadedAt.UTC().Format(TimeLayout),
	}
	if u.Withdrawn() {
		f.WithdrawnAt = new(u.WithdrawnAt.UTC().Format(TimeLayout))
	}
	return f
}

func newFiles(game store.GameRef, uploads []store.Upload) []File {
	files := make([]File, 0, len(uploads))
	for _, u := range uploads {
		files = append(files, newFile(game, u))
	}
	return files
}

// pathNumber returns the integer in the named part of the path. A number
// has one spelling in a path: its digits, with no sign or leading zeros.
func pathNumber(r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n >= 0 && strconv.FormatInt(n, 10) == raw
}

// folder returns the folder the request's path names, or responds 404.
// Whether the game has that turn and player is checked by the store.
func folder(w http.ResponseWriter, r *http.Request) (store.Folder, bool) {
	var f store.Folder
	if r.PathValue("turn") != "" {
		turn, ok := pathNumber(r, "turn")
		if !ok {
			writeError(w, http.StatusNotFound, msgNotFound)
			return f, false
		}
		f.Turn = &turn
	}
	if r.PathValue("id") != "" {
		player, ok := pathNumber(r, "id")
		if !ok || player == 0 {
			writeError(w, http.StatusNotFound, msgNotFound)
			return f, false
		}
		f.Player = &player
	}
	return f, true
}

// includeWithdrawn reads the withdrawn query parameter, or responds 400.
func includeWithdrawn(w http.ResponseWriter, r *http.Request) (include, ok bool) {
	q := r.URL.Query()
	if !q.Has("withdrawn") {
		return false, true
	}
	if q.Get("withdrawn") != "include" {
		writeError(w, http.StatusBadRequest, msgBadWithdrawn)
		return false, false
	}
	return true, true
}

func unknownFolder(err error) bool {
	return errors.Is(err, store.ErrUnknownTurn) || errors.Is(err, store.ErrUnknownPlayer)
}

func (h *Handler) listFiles(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	f, ok := folder(w, r)
	if !ok {
		return
	}
	withdrawn, ok := includeWithdrawn(w, r)
	if !ok {
		return
	}
	uploads, err := h.db.FolderUploads(r.Context(), gameID, f, withdrawn)
	if unknownFolder(err) {
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	} else if err != nil {
		h.logger.Error("api: list files", "email", account.Email, "game", game.String(), "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	writeJSON(w, http.StatusOK, FileList{System: game.System, Game: game.Code, Turn: f.Turn, Player: f.Player, Files: newFiles(game, uploads)})
}

func (h *Handler) uploadFile(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	f, ok := folder(w, r)
	if !ok {
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || (mt != "text/plain" && mt != "application/octet-stream") {
		writeError(w, http.StatusUnsupportedMediaType, msgBadFileType)
		return
	}
	name := r.URL.Query().Get("name")
	if err := names.File(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error()+msgFileNameSuffix)
		return
	}
	// The limit is enforced while reading: no more than it is buffered.
	contents, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxUpload))
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, msgFileTooLarge+strconv.FormatInt(h.maxUpload, 10))
		return
	} else if err != nil {
		writeError(w, http.StatusBadRequest, "the file could not be read")
		return
	}
	if err := names.Text(contents); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := h.db.CreateUpload(r.Context(), store.NewUpload{
		GameID: gameID, Folder: f, Name: name, Contents: contents,
		AccountID: account.ID, Email: account.Email, Now: h.now(),
	})
	if unknownFolder(err) {
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	} else if err != nil {
		h.logger.Error("api: upload file", "email", account.Email, "game", game.String(), "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	// The contents are never logged.
	h.logger.Info("api: uploaded file", "email", account.Email, "game", game.String(), "upload_id", u.ID, "name", u.Name, "size", u.Size, "sha256", u.SHA256)
	writeJSON(w, http.StatusCreated, newFile(game, u))
}

func (h *Handler) listUploads(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	var after int64
	if q := r.URL.Query(); q.Has("after") {
		raw := q.Get("after")
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != raw {
			writeError(w, http.StatusBadRequest, msgBadAfter)
			return
		}
		after = n
	}
	withdrawn, ok := includeWithdrawn(w, r)
	if !ok {
		return
	}
	uploads, err := h.db.GameUploads(r.Context(), gameID, after, withdrawn)
	if err != nil {
		h.logger.Error("api: list uploads", "email", account.Email, "game", game.String(), "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	writeJSON(w, http.StatusOK, UploadList{System: game.System, Game: game.Code, Uploads: newFiles(game, uploads)})
}

// uploadID returns the upload ID in the request's path, or responds 404.
func uploadID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathNumber(r, "upload")
	if !ok || id == 0 {
		writeError(w, http.StatusNotFound, msgNotFound)
		return 0, false
	}
	return id, true
}

func (h *Handler) getUpload(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	id, ok := uploadID(w, r)
	if !ok {
		return
	}
	u, found, err := h.db.GameUpload(r.Context(), gameID, id)
	if err != nil {
		h.logger.Error("api: get upload", "email", account.Email, "game", game.String(), "upload_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	} else if !found {
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	}
	writeJSON(w, http.StatusOK, newFile(game, u))
}

func (h *Handler) downloadUpload(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	id, ok := uploadID(w, r)
	if !ok {
		return
	}
	normalize, problem := parseNormalization(r.URL.Query())
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	u, contents, found, err := h.db.GameUploadContents(r.Context(), gameID, id)
	if err != nil {
		h.logger.Error("api: download", "email", account.Email, "game", game.String(), "upload_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	} else if !found {
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	}
	// Normalizing changes this response only, and the hash below stays the
	// stored file's.
	contents = normalize.apply(contents)
	// The file is served as a download, never as a page: it may be HTML and
	// must not run on this origin. Its name holds only safe ASCII.
	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Content-Disposition", `attachment; filename="`+u.Name+`"`)
	header.Set("Content-Length", strconv.Itoa(len(contents)))
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Content-SHA256", u.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(contents)
}

func (h *Handler) withdrawUpload(w http.ResponseWriter, r *http.Request, account store.Account) {
	game, gameID, ok := h.managedGame(w, r, account)
	if !ok {
		return
	}
	id, ok := uploadID(w, r)
	if !ok {
		return
	}
	u, err := h.db.WithdrawUpload(r.Context(), gameID, id, account.ID, h.now())
	switch {
	case errors.Is(err, store.ErrUnknownUpload):
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	case errors.Is(err, store.ErrNotUploader):
		writeError(w, http.StatusForbidden, msgNotYourUpload)
		return
	case err != nil:
		h.logger.Error("api: withdraw", "email", account.Email, "game", game.String(), "upload_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, msgServerError)
		return
	}
	h.logger.Info("api: withdrew file", "email", account.Email, "game", game.String(), "upload_id", id)
	writeJSON(w, http.StatusOK, newFile(game, u))
}
