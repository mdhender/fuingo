// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

const (
	ErrNotUploader   = cerrs.Error("the account did not upload the file")
	ErrUnknownTurn   = cerrs.Error("unknown turn")
	ErrUnknownUpload = cerrs.Error("unknown upload")
)

// Folder is a level of a game that holds files: the game itself if Turn is
// nil, a turn if only Turn is set, or a player's folder in that turn if
// Player is set too. Turn is the turn's number and Player the player's ID.
type Folder struct {
	Turn   *int64
	Player *int64
}

// Upload is the record of one uploaded file. Turn and Player say where it
// was put, as in Folder. WithdrawnAt is the zero time unless it was
// withdrawn.
type Upload struct {
	ID            int64
	Turn          *int64
	Player        *int64
	Name          string
	Size          int64
	SHA256        string // lowercase hex
	UploaderEmail string // as it was when the file was uploaded
	UploadedAt    time.Time
	WithdrawnAt   time.Time
}

// Withdrawn reports whether the upload was withdrawn.
func (u Upload) Withdrawn() bool {
	return !u.WithdrawnAt.IsZero()
}

// NewUpload describes a file to store. The caller has checked the name and
// the contents.
type NewUpload struct {
	GameID   int64
	Folder   Folder
	Name     string
	Contents []byte
	// AccountID and Email identify the uploader.
	AccountID int64
	Email     string
	Now       time.Time
}

const selectUploads = `
SELECT u.id, t.turn_no, u.player_id, u.name, c.size, u.sha256, u.uploader_email, u.uploaded_at, u.withdrawn_at
FROM uploads u
JOIN file_contents c ON c.sha256 = u.sha256
LEFT JOIN turns t ON t.id = u.turn_id
`

func scanUpload(stmt *sqlite.Stmt) Upload {
	u := Upload{
		ID:            stmt.ColumnInt64(0),
		Name:          stmt.ColumnText(3),
		Size:          stmt.ColumnInt64(4),
		SHA256:        stmt.ColumnText(5),
		UploaderEmail: stmt.ColumnText(6),
		UploadedAt:    parseTime(stmt.ColumnText(7)),
	}
	if stmt.ColumnType(1) != sqlite.TypeNull {
		u.Turn = new(stmt.ColumnInt64(1))
	}
	if stmt.ColumnType(2) != sqlite.TypeNull {
		u.Player = new(stmt.ColumnInt64(2))
	}
	if stmt.ColumnType(8) != sqlite.TypeNull {
		u.WithdrawnAt = parseTime(stmt.ColumnText(8))
	}
	return u
}

// folderIDs returns the row IDs of the folder's turn and player, each nil
// (NULL) if the folder has none. It fails with ErrUnknownTurn if the game
// has no such turn and ErrUnknownPlayer if it has no such player, active or
// not.
func folderIDs(conn *sqlite.Conn, gameID int64, f Folder) (turnID, playerID any, err error) {
	if f.Turn == nil {
		if f.Player != nil {
			return nil, nil, ErrUnknownTurn
		}
		return nil, nil, nil
	}
	err = sqlitex.Execute(conn, "SELECT id FROM turns WHERE game_id = ? AND turn_no = ?;", &sqlitex.ExecOptions{
		Args: []any{gameID, *f.Turn},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			turnID = stmt.ColumnInt64(0)
			return nil
		},
	})
	if err != nil {
		return nil, nil, err
	} else if turnID == nil {
		return nil, nil, ErrUnknownTurn
	}
	if f.Player == nil {
		return turnID, nil, nil
	}
	err = sqlitex.Execute(conn, "SELECT id FROM game_players WHERE game_id = ? AND id = ?;", &sqlitex.ExecOptions{
		Args: []any{gameID, *f.Player},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			playerID = stmt.ColumnInt64(0)
			return nil
		},
	})
	if err != nil {
		return nil, nil, err
	} else if playerID == nil {
		return nil, nil, ErrUnknownPlayer
	}
	return turnID, playerID, nil
}

// CreateUpload stores the file in the game's folder and returns its record.
// The contents are kept once, under their SHA-256, however many uploads hold
// the same bytes; contents already stored are not rewritten. The contents
// and the record are stored in one transaction. It fails with
// ErrUnknownTurn or ErrUnknownPlayer if the game has no such folder.
func (s *Store) CreateUpload(ctx context.Context, n NewUpload) (u Upload, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer s.pool.Put(conn)
	// Taking the write lock first makes upload IDs increase in the order
	// the uploads become visible, so a client that has seen an ID has seen
	// every lower one.
	end, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return Upload{}, err
	}
	defer end(&err)

	turnID, playerID, err := folderIDs(conn, n.GameID, n.Folder)
	if err != nil {
		return Upload{}, err
	}
	sum := sha256.Sum256(n.Contents)
	hash := hex.EncodeToString(sum[:])
	now := n.Now.UTC().Format(timeLayout)
	err = sqlitex.Execute(conn, `
INSERT INTO file_contents (sha256, size, contents, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (sha256) DO NOTHING;`, &sqlitex.ExecOptions{
		Args: []any{hash, len(n.Contents), n.Contents, now},
	})
	if err != nil {
		return Upload{}, err
	}
	err = sqlitex.Execute(conn, `
INSERT INTO uploads (system_id, game_id, turn_id, player_id, name, sha256, uploader_account_id, uploader_email, uploaded_at)
SELECT g.system_id, g.id, ?2, ?3, ?4, ?5, ?6, ?7, ?8 FROM games g WHERE g.id = ?1;`, &sqlitex.ExecOptions{
		Args: []any{n.GameID, turnID, playerID, n.Name, hash, n.AccountID, n.Email, now},
	})
	if err != nil {
		return Upload{}, err
	} else if conn.Changes() == 0 {
		return Upload{}, ErrUnknownGame
	}
	return Upload{
		ID:            conn.LastInsertRowID(),
		Turn:          n.Folder.Turn,
		Player:        n.Folder.Player,
		Name:          n.Name,
		Size:          int64(len(n.Contents)),
		SHA256:        hash,
		UploaderEmail: n.Email,
		UploadedAt:    parseTime(now),
	}, nil
}

// FolderUploads returns the uploads in exactly that folder of the game, not
// in the folders under it, in the order they arrived. Withdrawn uploads are
// left out unless withdrawn is set. It fails with ErrUnknownTurn or
// ErrUnknownPlayer if the game has no such folder.
func (s *Store) FolderUploads(ctx context.Context, gameID int64, f Folder, withdrawn bool) (uploads []Upload, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	turnID, playerID, err := folderIDs(conn, gameID, f)
	if err != nil {
		return nil, err
	}
	err = sqlitex.Execute(conn, selectUploads+`
WHERE u.game_id = ?1 AND u.turn_id IS ?2 AND u.player_id IS ?3 AND (?4 OR u.withdrawn_at IS NULL)
ORDER BY u.id;`, &sqlitex.ExecOptions{
		Args: []any{gameID, turnID, playerID, withdrawn},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			uploads = append(uploads, scanUpload(stmt))
			return nil
		},
	})
	return uploads, err
}

// GameUploads returns the game's uploads at every level whose IDs are
// greater than after, in order of ID. Withdrawn uploads are left out unless
// withdrawn is set.
func (s *Store) GameUploads(ctx context.Context, gameID, after int64, withdrawn bool) ([]Upload, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	var uploads []Upload
	err = sqlitex.Execute(conn, selectUploads+`
WHERE u.game_id = ?1 AND u.id > ?2 AND (?3 OR u.withdrawn_at IS NULL)
ORDER BY u.id;`, &sqlitex.ExecOptions{
		Args: []any{gameID, after, withdrawn},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			uploads = append(uploads, scanUpload(stmt))
			return nil
		},
	})
	return uploads, err
}

// GameUpload returns the game's upload with the given ID, withdrawn or
// not. It reports false if the game has none.
func (s *Store) GameUpload(ctx context.Context, gameID, id int64) (Upload, bool, error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Upload{}, false, err
	}
	defer s.pool.Put(conn)
	return gameUpload(conn, gameID, id)
}

func gameUpload(conn *sqlite.Conn, gameID, id int64) (u Upload, found bool, err error) {
	err = sqlitex.Execute(conn, selectUploads+"WHERE u.game_id = ? AND u.id = ?;", &sqlitex.ExecOptions{
		Args: []any{gameID, id},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			u, found = scanUpload(stmt), true
			return nil
		},
	})
	return u, found, err
}

// GameUploadContents returns the game's upload with the given ID and its
// contents, exactly as uploaded. It reports false if the game has no such
// upload. Contents are reached only through an upload of a game, never by
// their hash.
func (s *Store) GameUploadContents(ctx context.Context, gameID, id int64) (u Upload, contents []byte, found bool, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Upload{}, nil, false, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	u, found, err = gameUpload(conn, gameID, id)
	if err != nil || !found {
		return Upload{}, nil, false, err
	}
	err = sqlitex.Execute(conn, "SELECT contents FROM file_contents WHERE sha256 = ?;", &sqlitex.ExecOptions{
		Args: []any{u.SHA256},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			contents = make([]byte, stmt.ColumnLen(0))
			stmt.ColumnBytes(0, contents)
			return nil
		},
	})
	return u, contents, true, err
}

// WithdrawUpload withdraws the game's upload with the given ID and returns
// its record. Only the account that uploaded it may: any other fails with
// ErrNotUploader, and an ID the game does not have with ErrUnknownUpload.
// Withdrawing a withdrawn upload keeps the time it was first withdrawn.
// Nothing is deleted.
func (s *Store) WithdrawUpload(ctx context.Context, gameID, id, accountID int64, now time.Time) (u Upload, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	err = sqlitex.Execute(conn, `
UPDATE uploads SET withdrawn_at = coalesce(withdrawn_at, ?)
WHERE id = ? AND game_id = ? AND uploader_account_id = ?;`, &sqlitex.ExecOptions{
		Args: []any{now.UTC().Format(timeLayout), id, gameID, accountID},
	})
	if err != nil {
		return Upload{}, err
	}
	changed := conn.Changes() > 0
	u, found, err := gameUpload(conn, gameID, id)
	if err != nil {
		return Upload{}, err
	} else if !found {
		return Upload{}, ErrUnknownUpload
	} else if !changed {
		return Upload{}, ErrNotUploader
	}
	return u, nil
}
