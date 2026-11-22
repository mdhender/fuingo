// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/mdhender/fuingo/internal/names"
	"github.com/mdhender/fuingo/internal/store"
)

// maxManageForm bounds the form of a GM's change to a turn or a player.
const maxManageForm = 4 << 10

// gameRow is one game in a list of games.
type gameRow struct {
	Code, URL, Role string
	Inactive        bool
}

// systemGroup is a system and the games in it that the account may reach.
type systemGroup struct {
	Code, URL string
	Games     []gameRow
}

// homePage is the data of home.html: the games an account may reach.
type homePage struct {
	chrome
	Heading string
	Systems []systemGroup
}

func groupGames(games []store.Access) []systemGroup {
	var groups []systemGroup
	for _, a := range games {
		if len(groups) == 0 || groups[len(groups)-1].Code != a.Game.System {
			groups = append(groups, systemGroup{Code: a.Game.System, URL: systemURL(a.Game.System)})
		}
		row := gameRow{Code: a.Game.Code, URL: gameURL(a.Game), Inactive: !a.Active}
		switch {
		case a.Manager && a.Player != 0:
			row.Role = "GM and player"
		case a.Manager:
			row.Role = "GM"
		default:
			row.Role = "Player"
		}
		g := &groups[len(groups)-1]
		g.Games = append(g.Games, row)
	}
	return groups
}

// home lists the account's games. An account with one game lands in it.
func (p *pages) home(w http.ResponseWriter, r *http.Request, account store.Account) {
	games, err := p.db.AccountGames(r.Context(), account.ID)
	if err != nil {
		p.serverError(w, r, "list games", err)
		return
	}
	if len(games) == 1 {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, gameURL(games[0].Game), http.StatusSeeOther)
		return
	}
	page := homePage{chrome: newChrome(account, "Games"), Heading: "Your games", Systems: groupGames(games)}
	page.Crumbs = nil
	p.render(w, http.StatusOK, "home.html", page)
}

// system lists the games of one system that the account may reach. A
// system with none is not found.
func (p *pages) system(w http.ResponseWriter, r *http.Request, account store.Account) {
	system, err := names.Code(r.PathValue("system"))
	if err != nil {
		p.notFound(w, r, account)
		return
	}
	games, err := p.db.AccountGames(r.Context(), account.ID)
	if err != nil {
		p.serverError(w, r, "list games", err)
		return
	}
	games = slices.DeleteFunc(games, func(a store.Access) bool { return a.Game.System != system })
	if len(games) == 0 {
		p.notFound(w, r, account)
		return
	}
	p.render(w, http.StatusOK, "home.html", homePage{
		chrome:  newChrome(account, system, crumb{Name: system}),
		Heading: system, Systems: groupGames(games),
	})
}

func gameCrumbs(g store.GameRef, more ...crumb) []crumb {
	return append([]crumb{{Name: g.System, URL: systemURL(g.System)}, {Name: g.Code, URL: gameURL(g)}}, more...)
}

// turnRow is one turn in a game's list.
type turnRow struct {
	Number  int64
	URL     string
	Closed  bool
	Created string
}

// gamePage is the data of game.html.
type gamePage struct {
	chrome
	Game     store.GameRef
	Inactive bool
	Manager  bool
	Files    folderView
	Turns    []turnRow
	// NextTurn is the number of the turn a GM may create, at TurnsURL.
	NextTurn   int64
	TurnsURL   string
	PlayersURL string
}

func (p *pages) gamePage(w http.ResponseWriter, r *http.Request, account store.Account) {
	pl, ok := p.placeFromPath(w, r, account)
	if !ok {
		return
	}
	a := pl.access
	files, err := p.folder(r, pl)
	if err != nil {
		p.serverError(w, r, "list folder", err)
		return
	}
	turns, err := p.db.Turns(r.Context(), a.GameID)
	if err != nil {
		p.serverError(w, r, "list turns", err)
		return
	}
	crumbs := gameCrumbs(a.Game)
	crumbs[len(crumbs)-1].URL = ""
	page := gamePage{
		chrome: newChrome(account, a.Game.System+" / "+a.Game.Code, crumbs...),
		Game:   a.Game, Inactive: !a.Active, Manager: a.Manager, Files: files,
		NextTurn: int64(len(turns)), TurnsURL: gameURL(a.Game) + "/turns", PlayersURL: gameURL(a.Game) + "/players",
	}
	// The newest turn is listed first: it is the one most wanted.
	for _, t := range slices.Backward(turns) {
		page.Turns = append(page.Turns, turnRow{Number: t.Number, URL: turnURL(a.Game, t.Number), Closed: t.Closed(), Created: t.CreatedAt.UTC().Format(pageTimeLayout)})
	}
	p.render(w, http.StatusOK, "game.html", page)
}

// playerRow is one player's folder in a GM's view of a turn, or one player
// in the GM's list of them.
type playerRow struct {
	ID            int64
	Email         string
	Active        bool
	AccountActive bool
	// Files is how many files in the folder are not withdrawn.
	Files int
	URL   string
	// EnableURL or DisableURL is set in the list of players.
	EnableURL, DisableURL string
}

// turnPage is the data of turn.html.
type turnPage struct {
	chrome
	Game     store.GameRef
	Turn     int64
	Closed   bool
	ClosedAt string
	Manager  bool
	Files    folderView
	// Mine is the account's own folder in the turn, for a player.
	Mine *folderView
	// Players are every player's folder, for a GM.
	Players           []playerRow
	CloseURL, OpenURL string
}

func (p *pages) turnPage(w http.ResponseWriter, r *http.Request, account store.Account) {
	pl, ok := p.placeFromPath(w, r, account)
	if !ok {
		return
	}
	a, turn := pl.access, pl.turn
	files, err := p.folder(r, pl)
	if err != nil {
		p.serverError(w, r, "list folder", err)
		return
	}
	page := turnPage{
		chrome: newChrome(account, fmt.Sprintf("%s / %s / turn %d", a.Game.System, a.Game.Code, turn.Number), gameCrumbs(a.Game, crumb{Name: fmt.Sprintf("Turn %d", turn.Number)})...),
		Game:   a.Game, Turn: turn.Number, Closed: turn.Closed(), Manager: a.Manager, Files: files,
		CloseURL: turnURL(a.Game, turn.Number) + "/close", OpenURL: turnURL(a.Game, turn.Number) + "/open",
	}
	if turn.Closed() {
		page.ClosedAt = turn.ClosedAt.UTC().Format(pageTimeLayout)
	}
	if a.Manager {
		players, err := p.db.GamePlayers(r.Context(), a.GameID)
		if err != nil {
			p.serverError(w, r, "list players", err)
			return
		}
		counts, err := p.db.TurnFolderCounts(r.Context(), a.GameID, turn.Number)
		if err != nil {
			p.serverError(w, r, "count files", err)
			return
		}
		for _, player := range players {
			page.Players = append(page.Players, playerRow{
				ID: player.ID, Email: player.Email, Active: player.Active && player.AccountActive, Files: counts[player.ID],
				URL: folderURL(a.Game, store.Folder{Turn: &turn.Number, Player: &player.ID}),
			})
		}
	} else if a.Player != 0 {
		mine, found, err := p.find(r, account, a, store.Folder{Turn: &turn.Number, Player: &a.Player})
		if err == nil && found {
			var v folderView
			if v, err = p.folder(r, mine); err == nil {
				page.Mine = &v
			}
		}
		if err != nil {
			p.serverError(w, r, "list folder", err)
			return
		}
	}
	p.render(w, http.StatusOK, "turn.html", page)
}

// folderPage shows one player's folder in a turn: to a GM, or to that
// player.
func (p *pages) folderPage(w http.ResponseWriter, r *http.Request, account store.Account) {
	pl, ok := p.placeFromPath(w, r, account)
	if !ok {
		return
	}
	files, err := p.folder(r, pl)
	if err != nil {
		p.serverError(w, r, "list folder", err)
		return
	}
	g, turn := pl.access.Game, pl.turn.Number
	last := "Your folder"
	if pl.access.Manager {
		last = fmt.Sprintf("Player %d", *pl.folder.Player)
	}
	p.render(w, http.StatusOK, "folder.html", folderPageData{
		chrome: newChrome(account, files.Where, gameCrumbs(g, crumb{Name: fmt.Sprintf("Turn %d", turn), URL: turnURL(g, turn)}, crumb{Name: last})...),
		Files:  files,
	})
}

// managed is access for a route only a GM of the game may use. To anyone
// else the route does not exist.
func (p *pages) managed(w http.ResponseWriter, r *http.Request, account store.Account) (store.Access, bool) {
	a, ok := p.access(w, r, account)
	if ok && !a.Manager {
		p.notFound(w, r, account)
		return store.Access{}, false
	}
	return a, ok
}

func readForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxManageForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return false
	}
	return true
}

func seeOther(w http.ResponseWriter, r *http.Request, url string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// createTurn creates the turn whose number the form gives, which must be
// the game's next, so that a form sent twice creates one turn.
func (p *pages) createTurn(w http.ResponseWriter, r *http.Request, account store.Account) {
	a, ok := p.managed(w, r, account)
	if !ok || !readForm(w, r) {
		return
	}
	raw := r.PostForm.Get("turn")
	number, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || number < 0 || strconv.FormatInt(number, 10) != raw {
		p.message(w, r, account, http.StatusBadRequest, "Refused", "The turn's number must be an integer, 0 or more.")
		return
	}
	turn, err := p.db.CreateTurn(r.Context(), a.GameID, number, p.now())
	if errors.Is(err, store.ErrNotNextTurn) {
		p.message(w, r, account, http.StatusConflict, "Refused", fmt.Sprintf("Turn %d was not created: the game's next turn is %d.", number, turn.Number))
		return
	} else if err != nil {
		p.serverError(w, r, "create turn", err)
		return
	}
	p.logger.Info("created turn", "email", account.Email, "game", a.Game.String(), "turn", turn.Number)
	seeOther(w, r, turnURL(a.Game, turn.Number))
}

// setTurnClosed closes or reopens a turn.
func (p *pages) setTurnClosed(closed bool) func(http.ResponseWriter, *http.Request, store.Account) {
	return func(w http.ResponseWriter, r *http.Request, account store.Account) {
		a, ok := p.managed(w, r, account)
		if !ok {
			return
		}
		number, ok := pathNumber(r, "turn")
		if !ok {
			p.notFound(w, r, account)
			return
		}
		_, found, err := p.db.SetTurnClosed(r.Context(), a.GameID, number, closed, p.now())
		if err != nil {
			p.serverError(w, r, "set turn closed", err)
			return
		} else if !found {
			p.notFound(w, r, account)
			return
		}
		p.logger.Info("set turn closed", "email", account.Email, "game", a.Game.String(), "turn", number, "closed", closed)
		seeOther(w, r, turnURL(a.Game, number))
	}
}

// playersPage is the data of players.html: a GM's list of a game's players.
type playersPage struct {
	chrome
	Game    store.GameRef
	Players []playerRow
	AddURL  string
	Notice  string
	Error   string
}

// showPlayers renders the game's players with a notice or an error.
func (p *pages) showPlayers(w http.ResponseWriter, r *http.Request, account store.Account, a store.Access, status int, notice, problem string) {
	players, err := p.db.GamePlayers(r.Context(), a.GameID)
	if err != nil {
		p.serverError(w, r, "list players", err)
		return
	}
	base := gameURL(a.Game) + "/players"
	page := playersPage{
		chrome: newChrome(account, a.Game.System+" / "+a.Game.Code+" / players", gameCrumbs(a.Game, crumb{Name: "Players"})...),
		Game:   a.Game, AddURL: base, Notice: notice, Error: problem,
	}
	for _, player := range players {
		row := playerRow{ID: player.ID, Email: player.Email, Active: player.Active, AccountActive: player.AccountActive}
		if player.Active {
			row.DisableURL = base + "/" + strconv.FormatInt(player.ID, 10) + "/disable"
		} else {
			row.EnableURL = base + "/" + strconv.FormatInt(player.ID, 10) + "/enable"
		}
		page.Players = append(page.Players, row)
	}
	p.render(w, status, "players.html", page)
}

func (p *pages) playersPage(w http.ResponseWriter, r *http.Request, account store.Account) {
	if a, ok := p.managed(w, r, account); ok {
		p.showPlayers(w, r, account, a, http.StatusOK, "", "")
	}
}

// addPlayer gives the form's email access to the game. An email without an
// account gets one, and its invitation. That is all a GM does to an
// account.
func (p *pages) addPlayer(w http.ResponseWriter, r *http.Request, account store.Account) {
	a, ok := p.managed(w, r, account)
	if !ok || !readForm(w, r) {
		return
	}
	email, err := names.Email(r.PostForm.Get("email"))
	if err != nil {
		p.showPlayers(w, r, account, a, http.StatusBadRequest, "", "Enter the player's email address, without spaces.")
		return
	}
	added, err := p.db.AddGamePlayer(r.Context(), a.GameID, email)
	if errors.Is(err, store.ErrPlayerExists) {
		p.showPlayers(w, r, account, a, http.StatusConflict, "", email+" is already a player in this game.")
		return
	} else if err != nil {
		p.serverError(w, r, "add player", err)
		return
	}
	// A new account gets its invitation. The player stays added if the mail
	// fails: the owner can ask for a new link on the reset page.
	invitation := "none"
	notice := fmt.Sprintf("Added %s as player %d. The account already existed, so no invitation was sent.", email, added.ID)
	if added.NewAccount {
		invitation = "sent"
		notice = fmt.Sprintf("Added %s as player %d and mailed an invitation.", email, added.ID)
		if err := p.links.Send(r.Context(), added.AccountID, email, store.LinkInvitation); err != nil {
			invitation = "failed"
			notice = fmt.Sprintf("Added %s as player %d, but the invitation could not be mailed. They can ask for one on the sign-in page, under “Forgot your password”.", email, added.ID)
			p.logger.Error("send invitation", "email", account.Email, "game", a.Game.String(), "player", email, "err", err)
		}
	}
	p.logger.Info("added player", "email", account.Email, "game", a.Game.String(), "player", email, "player_id", added.ID, "invitation", invitation)
	p.showPlayers(w, r, account, a, http.StatusCreated, notice, "")
}

// setPlayerActive enables or disables a player of the game.
func (p *pages) setPlayerActive(active bool) func(http.ResponseWriter, *http.Request, store.Account) {
	return func(w http.ResponseWriter, r *http.Request, account store.Account) {
		a, ok := p.managed(w, r, account)
		if !ok {
			return
		}
		id, ok := pathNumber(r, "id")
		if !ok || id == 0 {
			p.notFound(w, r, account)
			return
		}
		found, err := p.db.SetGamePlayerActive(r.Context(), a.GameID, id, active)
		if err != nil {
			p.serverError(w, r, "set player active", err)
			return
		} else if !found {
			p.notFound(w, r, account)
			return
		}
		p.logger.Info("set player active", "email", account.Email, "game", a.Game.String(), "player_id", id, "active", active)
		seeOther(w, r, gameURL(a.Game)+"/players")
	}
}
