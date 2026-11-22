// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/store"
)

const (
	// DefaultMaxPlayerUpload is the most bytes a file a player uploads on
	// the web may hold, unless configured.
	DefaultMaxPlayerUpload = 64 << 10
	// DefaultMaxGMUpload is the most bytes a file a GM uploads on the web
	// may hold, unless configured.
	DefaultMaxGMUpload = 1 << 20
	// uploadFormOverhead is how much larger than its file an upload's
	// request may be: the multipart boundaries and headers.
	uploadFormOverhead = 16 << 10
	// pageTimeLayout formats the times shown on pages.
	pageTimeLayout = "2006-01-02 15:04 UTC"
)

// pages serves the signed-in pages: the games an account may reach, their
// turns and folders, and the files in them.
//
// Every route needs a session. What an account may not reach gets the same
// response as what does not exist. File contents are never logged.
type pages struct {
	sessions *sessions
	db       *store.Store
	logger   *slog.Logger
	now      func() time.Time
	links    *links.Service
	// maxPlayer and maxGM are the most bytes a player's and a GM's upload
	// may hold.
	maxPlayer, maxGM int64
}

// signedIn runs next with the account signed in on the request. Anyone else
// is sent to the sign-in page.
func (p *pages) signedIn(next func(http.ResponseWriter, *http.Request, store.Account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, ok, err := p.sessions.account(w, r)
		if err != nil {
			p.serverError(w, r, "find session", err)
			return
		} else if !ok {
			w.Header().Set("Cache-Control", "no-store")
			if isHTMX(r) {
				// HTMX would follow a redirect and put the sign-in page
				// inside the page; this sends the whole browser there.
				// The way back is the page the browser shows, not the
				// fragment's address.
				next := ""
				if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
					next = u.RequestURI()
				}
				w.Header().Set("HX-Redirect", signInURL(next))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// Only a page that was asked for is worth coming back to: a
			// form's address would answer a GET with nothing.
			next := ""
			if r.Method == http.MethodGet {
				next = r.URL.RequestURI()
			}
			http.Redirect(w, r, signInURL(next), http.StatusSeeOther)
			return
		}
		next(w, r, account)
	}
}

// isHTMX reports whether HTMX made the request and so expects a fragment.
// The header chooses only the shape of the response, never what is allowed.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// crumb is one step of the breadcrumbs. The last has no URL.
type crumb struct {
	Name, URL string
}

// chrome is the data every signed-in page shares.
type chrome struct {
	HTMX, Tailwind, SignOutPath string
	Title                       string
	Email                       string
	Crumbs                      []crumb
}

func newChrome(account store.Account, title string, crumbs ...crumb) chrome {
	return chrome{HTMX: HTMXPath, Tailwind: TailwindURL, SignOutPath: SignOutPath, Title: title, Email: account.Email,
		Crumbs: append([]crumb{{Name: "Games", URL: "/"}}, crumbs...)}
}

func setPageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

// render writes the named template, a whole page or a fragment.
func (p *pages) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		p.logger.Error("page", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setPageHeaders(w)
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// messagePage is the data of message.html: a page that only says something.
type messagePage struct {
	chrome
	Message string
}

// message answers with a page that only says something, or, to HTMX, by
// having the browser load the page again, which then shows how things are.
func (p *pages) message(w http.ResponseWriter, r *http.Request, account store.Account, status int, title, message string) {
	if isHTMX(r) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(status)
		return
	}
	p.render(w, status, "message.html", messagePage{chrome: newChrome(account, title), Message: message})
}

// notFound is the one answer to everything that does not exist or that the
// account may not reach.
func (p *pages) notFound(w http.ResponseWriter, r *http.Request, account store.Account) {
	p.message(w, r, account, http.StatusNotFound, "Not found", "There is nothing here, or it is not yours to see.")
}

func (p *pages) serverError(w http.ResponseWriter, _ *http.Request, what string, err error) {
	p.logger.Error("pages: "+what, "err", err)
	http.Error(w, "the request could not be completed; try again later", http.StatusInternalServerError)
}

func systemURL(system string) string {
	return "/systems/" + url.PathEscape(system)
}

func gameURL(g store.GameRef) string {
	return systemURL(g.System) + "/games/" + url.PathEscape(g.Code)
}

func turnURL(g store.GameRef, turn int64) string {
	return gameURL(g) + "/turns/" + strconv.FormatInt(turn, 10)
}

// folderURL is the address of a folder: the game, a turn, or a player's
// folder in a turn.
func folderURL(g store.GameRef, f store.Folder) string {
	switch {
	case f.Turn == nil:
		return gameURL(g)
	case f.Player == nil:
		return turnURL(g, *f.Turn)
	}
	return turnURL(g, *f.Turn) + "/players/" + strconv.FormatInt(*f.Player, 10)
}

func uploadURL(g store.GameRef, id int64) string {
	return gameURL(g) + "/uploads/" + strconv.FormatInt(id, 10)
}

// pathNumber returns the integer in the named part of the path. A number
// has one spelling in a path: its digits, with no sign or leading zeros.
func pathNumber(r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n >= 0 && strconv.FormatInt(n, 10) == raw
}

// formatBytes writes a size for a person.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	case n == 1:
		return "1 byte"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// access returns what the account may reach in the game the path names, or
// answers that there is nothing there.
func (p *pages) access(w http.ResponseWriter, r *http.Request, account store.Account) (store.Access, bool) {
	system, err1 := names.Code(r.PathValue("system"))
	code, err2 := names.Code(r.PathValue("game"))
	if err1 != nil || err2 != nil {
		p.notFound(w, r, account)
		return store.Access{}, false
	}
	a, found, err := p.db.GameAccess(r.Context(), account.ID, store.GameRef{System: system, Code: code})
	if err != nil {
		p.serverError(w, r, "find game", err)
		return store.Access{}, false
	} else if !found {
		p.notFound(w, r, account)
		return store.Access{}, false
	}
	return a, true
}

// place is a folder the account may read, and what is known of it.
type place struct {
	account store.Account
	access  store.Access
	folder  store.Folder
	// turn is the folder's turn, unless the folder is the game itself.
	turn store.Turn
	// playerEmail is the email of the folder's player, for a GM.
	playerEmail string
}

// mine reports whether the folder is the account's own, as a player.
func (pl place) mine() bool {
	return pl.folder.Player != nil && pl.access.Player != 0 && *pl.folder.Player == pl.access.Player
}

// find fills in the place for a folder of a game the account may reach. It
// reports false if the game has no such folder or the account may not read
// it: a player reads the game's and the turns' folders and their own.
func (p *pages) find(r *http.Request, account store.Account, a store.Access, f store.Folder) (place, bool, error) {
	pl := place{account: account, access: a, folder: f}
	if f.Turn == nil {
		return pl, f.Player == nil, nil
	}
	turn, found, err := p.db.FindTurn(r.Context(), a.GameID, *f.Turn)
	if err != nil || !found {
		return pl, false, err
	}
	pl.turn = turn
	if f.Player == nil {
		return pl, true, nil
	}
	if !a.Manager {
		return pl, pl.mine(), nil
	}
	players, err := p.db.GamePlayers(r.Context(), a.GameID)
	if err != nil {
		return pl, false, err
	}
	for _, player := range players {
		if player.ID == *f.Player {
			pl.playerEmail = player.Email
			return pl, true, nil
		}
	}
	return pl, false, nil
}

// placeFromPath is find for the folder the path names, answering the
// request itself if there is no such place.
func (p *pages) placeFromPath(w http.ResponseWriter, r *http.Request, account store.Account) (place, bool) {
	a, ok := p.access(w, r, account)
	if !ok {
		return place{}, false
	}
	var f store.Folder
	if r.PathValue("turn") != "" {
		turn, ok := pathNumber(r, "turn")
		if !ok {
			p.notFound(w, r, account)
			return place{}, false
		}
		f.Turn = &turn
	}
	if r.PathValue("id") != "" {
		player, ok := pathNumber(r, "id")
		if !ok || player == 0 {
			p.notFound(w, r, account)
			return place{}, false
		}
		f.Player = &player
	}
	pl, found, err := p.find(r, account, a, f)
	if err != nil {
		p.serverError(w, r, "find folder", err)
		return place{}, false
	} else if !found {
		p.notFound(w, r, account)
		return place{}, false
	}
	return pl, true
}

// uploadLimit returns the most bytes the account may upload to the place,
// and whether it may upload there at all: a GM to any folder of the game,
// and a player to their own folder while its turn is open.
func (p *pages) uploadLimit(pl place) (int64, bool) {
	switch {
	case pl.access.Manager:
		return p.maxGM, true
	case pl.mine() && !pl.turn.Closed():
		return p.maxPlayer, true
	}
	return 0, false
}

// fileView is one row of a folder's list.
type fileView struct {
	Name, Size, By, At string
	DownloadURL        string
	// WithdrawURL is empty if the viewer may not withdraw the file.
	WithdrawURL string
	// WithdrawnAt is empty unless the file was withdrawn.
	WithdrawnAt string
}

// receipt confirms an upload.
type receipt struct {
	Name, Size, At string
}

// folderView is the data of the "folder" template: a folder's files and,
// if the viewer may add to it, its drop zone.
type folderView struct {
	// ID is the element's id, which a fragment replaces.
	ID string
	// Title names the folder, and Where says where it is in full.
	Title, Where string
	Files        []fileView
	// UploadURL is empty if the viewer may not upload here.
	UploadURL string
	MaxBytes  int64
	MaxText   string
	// ClosedTo is set if the viewer's own folder takes no more files
	// because its turn is closed.
	ClosedTo bool
	Receipt  *receipt
	Notice   string
	Error    string
	// PageURL is the page that shows this folder.
	PageURL string
}

// folder builds the view of a place.
func (p *pages) folder(r *http.Request, pl place) (folderView, error) {
	game := pl.access.Game
	v := folderView{Where: game.System + " / " + game.Code}
	switch {
	case pl.folder.Turn == nil:
		v.ID, v.Title = "files-game", "Game files"
		v.PageURL = gameURL(game)
	case pl.folder.Player == nil:
		v.ID, v.Title = "files-turn", fmt.Sprintf("Turn %d files", *pl.folder.Turn)
		v.Where += fmt.Sprintf(" / turn %d", *pl.folder.Turn)
		v.PageURL = turnURL(game, *pl.folder.Turn)
	case pl.access.Manager:
		v.ID, v.Title = "files-player", fmt.Sprintf("Player %d: %s", *pl.folder.Player, pl.playerEmail)
		v.Where += fmt.Sprintf(" / turn %d / player %d (%s)", *pl.folder.Turn, *pl.folder.Player, pl.playerEmail)
		v.PageURL = folderURL(game, pl.folder)
	default:
		v.ID, v.Title = "files-player", fmt.Sprintf("Your files for turn %d", *pl.folder.Turn)
		v.Where += fmt.Sprintf(" / turn %d / your folder", *pl.folder.Turn)
		v.PageURL = turnURL(game, *pl.folder.Turn)
		v.ClosedTo = pl.turn.Closed()
	}
	if limit, ok := p.uploadLimit(pl); ok {
		v.UploadURL = folderURL(game, pl.folder) + "/files"
		v.MaxBytes, v.MaxText = limit, formatBytes(limit)
	}
	// Only a GM is shown withdrawn files.
	uploads, err := p.db.FolderUploads(r.Context(), pl.access.GameID, pl.folder, pl.access.Manager)
	if err != nil {
		return folderView{}, err
	}
	for _, u := range uploads {
		f := fileView{
			Name: u.Name, Size: formatBytes(u.Size), At: u.UploadedAt.UTC().Format(pageTimeLayout),
			DownloadURL: uploadURL(game, u.ID),
		}
		// A player is shown who a file is from, not the GM's address.
		switch {
		case u.UploaderID == pl.account.ID:
			f.By = "you"
		case pl.access.Manager:
			f.By = u.UploaderEmail
		default:
			f.By = "the GM"
		}
		if u.Withdrawn() {
			f.WithdrawnAt = u.WithdrawnAt.UTC().Format(pageTimeLayout)
		} else if u.UploaderID == pl.account.ID && (pl.access.Manager || (pl.mine() && !pl.turn.Closed())) {
			f.WithdrawURL = uploadURL(game, u.ID) + "/withdraw"
		}
		v.Files = append(v.Files, f)
	}
	return v, nil
}

// folderPageData is the data of folder.html, the page of one player's
// folder.
type folderPageData struct {
	chrome
	Files folderView
}

// respondFolder answers an upload or a withdrawal with the folder as it now
// is, carrying the receipt, notice, or error in change. HTMX gets the
// folder's fragment. A plain form post is sent back to the folder's page,
// or, refused, is told why.
func (p *pages) respondFolder(w http.ResponseWriter, r *http.Request, pl place, status int, change folderView) {
	if !isHTMX(r) {
		if status < 400 {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, folderPage(pl), http.StatusSeeOther)
			return
		}
		p.message(w, r, pl.account, status, "Refused", change.Error)
		return
	}
	// The place is read again: the request may have changed it, and a
	// refusal may mean the page was out of date.
	fresh, found, err := p.find(r, pl.account, pl.access, pl.folder)
	if err != nil {
		p.serverError(w, r, "find folder", err)
		return
	} else if !found {
		p.notFound(w, r, pl.account)
		return
	}
	v, err := p.folder(r, fresh)
	if err != nil {
		p.serverError(w, r, "list folder", err)
		return
	}
	v.Receipt, v.Notice, v.Error = change.Receipt, change.Notice, change.Error
	p.render(w, status, "folder", v)
}

// folderPage is the page that shows the place's folder to its account.
func folderPage(pl place) string {
	if pl.folder.Player != nil && !pl.access.Manager {
		return turnURL(pl.access.Game, *pl.folder.Turn)
	}
	return folderURL(pl.access.Game, pl.folder)
}

func (p *pages) refuseFolder(w http.ResponseWriter, r *http.Request, pl place, status int, message string) {
	p.respondFolder(w, r, pl, status, folderView{Error: message})
}

// upload stores the file in the form's "file" part in the folder the path
// names.
func (p *pages) upload(w http.ResponseWriter, r *http.Request, account store.Account) {
	pl, ok := p.placeFromPath(w, r, account)
	if !ok {
		return
	}
	limit, ok := p.uploadLimit(pl)
	if !ok {
		if pl.mine() {
			p.refuseFolder(w, r, pl, http.StatusConflict, fmt.Sprintf("Turn %d is closed, so it takes no more files.", pl.turn.Number))
			return
		}
		p.refuseFolder(w, r, pl, http.StatusForbidden, "Only the GM adds files here.")
		return
	}
	name, contents, status, problem := readUpload(w, r, limit)
	if problem == "" {
		if err := names.File(name); err != nil {
			status, problem = http.StatusBadRequest, "The file was not accepted: its name must be 1 to 48 letters, digits, hyphens, underscores, or periods, with no spaces, and must not start or end with a period."
		} else if err := names.Text(contents); err != nil {
			status, problem = http.StatusBadRequest, "The file was not accepted: "+err.Error()+"."
		}
	}
	if problem != "" {
		p.refuseFolder(w, r, pl, status, problem)
		return
	}
	game := pl.access.Game
	u, err := p.db.CreateUpload(r.Context(), store.NewUpload{
		GameID: pl.access.GameID, Folder: pl.folder, Name: name, Contents: contents,
		AccountID: account.ID, Email: account.Email, Now: p.now(),
		// The turn may have been closed since the place was read.
		OpenTurnOnly: !pl.access.Manager,
	})
	switch {
	case errors.Is(err, store.ErrTurnClosed):
		p.refuseFolder(w, r, pl, http.StatusConflict, fmt.Sprintf("Turn %d is closed, so it takes no more files.", pl.turn.Number))
		return
	case errors.Is(err, store.ErrUnknownTurn), errors.Is(err, store.ErrUnknownPlayer):
		p.notFound(w, r, account)
		return
	case err != nil:
		p.serverError(w, r, "upload", err)
		return
	}
	// The contents are never logged.
	p.logger.Info("uploaded file", "email", account.Email, "game", game.String(), "upload_id", u.ID, "name", u.Name, "size", u.Size, "sha256", u.SHA256)
	p.respondFolder(w, r, pl, http.StatusCreated, folderView{
		Receipt: &receipt{Name: u.Name, Size: formatBytes(u.Size), At: u.UploadedAt.UTC().Format(pageTimeLayout)},
	})
}

// readUpload reads the file in the "file" part of a multipart form, reading
// no more than limit bytes of it and one more. It returns the file's name
// and contents, or the status and message that refuse it.
func readUpload(w http.ResponseWriter, r *http.Request, limit int64) (name string, contents []byte, status int, problem string) {
	tooLarge := "The file was not accepted: it is larger than the limit of " + formatBytes(limit) + "."
	r.Body = http.MaxBytesReader(w, r.Body, limit+uploadFormOverhead)
	parts, err := r.MultipartReader()
	if err != nil {
		return "", nil, http.StatusBadRequest, "The file could not be read."
	}
	for {
		part, err := parts.NextPart()
		if _, large := errors.AsType[*http.MaxBytesError](err); large {
			return "", nil, http.StatusRequestEntityTooLarge, tooLarge
		} else if err != nil {
			// Including io.EOF: the form had no file.
			return "", nil, http.StatusBadRequest, "The file could not be read."
		}
		if part.FormName() != "file" {
			continue
		}
		name = part.FileName()
		contents, err = io.ReadAll(io.LimitReader(part, limit+1))
		if _, large := errors.AsType[*http.MaxBytesError](err); large || int64(len(contents)) > limit {
			return "", nil, http.StatusRequestEntityTooLarge, tooLarge
		} else if err != nil {
			return "", nil, http.StatusBadRequest, "The file could not be read."
		}
		return name, contents, 0, ""
	}
}

// visible reports whether the account may see the upload: a GM every file
// of the game, a player the files of the game, of its turns, and of their
// own folders that are not withdrawn.
func visible(a store.Access, u store.Upload) bool {
	if a.Manager {
		return true
	}
	return !u.Withdrawn() && (u.Player == nil || (a.Player != 0 && *u.Player == a.Player))
}

// download serves a file's contents exactly as stored.
func (p *pages) download(w http.ResponseWriter, r *http.Request, account store.Account) {
	a, ok := p.access(w, r, account)
	if !ok {
		return
	}
	id, ok := pathNumber(r, "upload")
	if !ok || id == 0 {
		p.notFound(w, r, account)
		return
	}
	u, contents, found, err := p.db.GameUploadContents(r.Context(), a.GameID, id)
	if err != nil {
		p.serverError(w, r, "download", err)
		return
	} else if !found || !visible(a, u) {
		p.notFound(w, r, account)
		return
	}
	// The file is served as a download, never as a page: it may be HTML and
	// must not run on this origin, which holds sessions. Its name holds
	// only safe ASCII.
	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Content-Disposition", `attachment; filename="`+u.Name+`"`)
	header.Set("Content-Length", strconv.Itoa(len(contents)))
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(contents)
}

// withdraw withdraws a file the account uploaded: a GM at any time, a
// player from their own folder while its turn is open.
func (p *pages) withdraw(w http.ResponseWriter, r *http.Request, account store.Account) {
	a, ok := p.access(w, r, account)
	if !ok {
		return
	}
	id, ok := pathNumber(r, "upload")
	if !ok || id == 0 {
		p.notFound(w, r, account)
		return
	}
	u, found, err := p.db.GameUpload(r.Context(), a.GameID, id)
	if err != nil {
		p.serverError(w, r, "withdraw", err)
		return
	} else if !found || !visible(a, u) {
		p.notFound(w, r, account)
		return
	}
	pl, found, err := p.find(r, account, a, store.Folder{Turn: u.Turn, Player: u.Player})
	if err != nil {
		p.serverError(w, r, "withdraw", err)
		return
	} else if !found {
		p.notFound(w, r, account)
		return
	}
	switch {
	case a.Manager:
		_, err = p.db.WithdrawUpload(r.Context(), a.GameID, id, account.ID, p.now())
	case pl.mine():
		_, err = p.db.WithdrawPlayerUpload(r.Context(), a.GameID, id, account.ID, a.Player, p.now())
	default:
		// A player withdraws only from their own folder.
		err = store.ErrNotUploader
	}
	switch {
	case errors.Is(err, store.ErrUnknownUpload):
		p.notFound(w, r, account)
	case errors.Is(err, store.ErrNotUploader):
		p.refuseFolder(w, r, pl, http.StatusForbidden, "Only the account that uploaded "+u.Name+" may withdraw it.")
	case errors.Is(err, store.ErrTurnClosed):
		p.refuseFolder(w, r, pl, http.StatusConflict, fmt.Sprintf("Turn %d is closed, so %s can no longer be withdrawn.", pl.turn.Number, u.Name))
	case err != nil:
		p.serverError(w, r, "withdraw", err)
	default:
		p.logger.Info("withdrew file", "email", account.Email, "game", a.Game.String(), "upload_id", id)
		p.respondFolder(w, r, pl, http.StatusOK, folderView{Notice: "Withdrew " + u.Name + "."})
	}
}
