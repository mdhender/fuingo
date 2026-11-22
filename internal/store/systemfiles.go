// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// A system-level file is an upload with no game. Only the administrator
// adds and withdraws them, so they have no uploader account. They are
// reached only through these functions, by their system: no game's
// functions return them.

// systemID returns the ID of the system with the code, or ErrUnknownSystem.
func systemID(conn *sqlite.Conn, system string) (id int64, err error) {
	err = sqlitex.Execute(conn, "SELECT id FROM systems WHERE code = ?;", &sqlitex.ExecOptions{
		Args: []any{system},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			id = stmt.ColumnInt64(0)
			return nil
		},
	})
	if err != nil {
		return 0, err
	} else if id == 0 {
		return 0, fmt.Errorf("%s: %w", system, ErrUnknownSystem)
	}
	return id, nil
}

// CreateSystemUpload stores the file at the system's level, as the
// administrator, and returns its record. The caller has checked the name
// and the contents. The contents are kept once, as for CreateUpload, and
// stored with the record in one transaction. It fails with
// ErrUnknownSystem if there is no such system.
func (s *Store) CreateSystemUpload(ctx context.Context, system, name string, contents []byte, now time.Time) (u Upload, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer s.pool.Put(conn)
	end, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return Upload{}, err
	}
	defer end(&err)
	id, err := systemID(conn, system)
	if err != nil {
		return Upload{}, err
	}
	at := now.UTC().Format(timeLayout)
	hash, err := storeContents(conn, contents, at)
	if err != nil {
		return Upload{}, err
	}
	err = sqlitex.Execute(conn, `
INSERT INTO uploads (system_id, name, sha256, uploader_email, uploaded_at) VALUES (?, ?, ?, '', ?);`, &sqlitex.ExecOptions{
		Args: []any{id, name, hash, at},
	})
	if err != nil {
		return Upload{}, err
	}
	return Upload{ID: conn.LastInsertRowID(), Name: name, Size: int64(len(contents)), SHA256: hash, UploadedAt: parseTime(at)}, nil
}

// SystemUploads returns the system's own files, not its games', in the
// order they arrived. Withdrawn files are left out unless withdrawn is
// set. It fails with ErrUnknownSystem if there is no such system.
func (s *Store) SystemUploads(ctx context.Context, system string, withdrawn bool) (uploads []Upload, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return nil, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	id, err := systemID(conn, system)
	if err != nil {
		return nil, err
	}
	err = sqlitex.Execute(conn, selectUploads+`
WHERE u.system_id = ?1 AND u.game_id IS NULL AND (?2 OR u.withdrawn_at IS NULL)
ORDER BY u.id;`, &sqlitex.ExecOptions{
		Args: []any{id, withdrawn},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			uploads = append(uploads, scanUpload(stmt))
			return nil
		},
	})
	return uploads, err
}

func systemUpload(conn *sqlite.Conn, system string, id int64) (u Upload, found bool, err error) {
	err = sqlitex.Execute(conn, selectUploads+`
WHERE u.id = ?1 AND u.game_id IS NULL AND u.system_id = (SELECT id FROM systems WHERE code = ?2);`, &sqlitex.ExecOptions{
		Args: []any{id, system},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			u, found = scanUpload(stmt), true
			return nil
		},
	})
	return u, found, err
}

// SystemUploadContents returns the system's own file with the given ID and
// its contents, exactly as uploaded. It reports false if the system has no
// such file, which includes a file of one of its games and, unless
// withdrawn is set, a withdrawn one.
func (s *Store) SystemUploadContents(ctx context.Context, system string, id int64, withdrawn bool) (u Upload, contents []byte, found bool, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Upload{}, nil, false, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	u, found, err = systemUpload(conn, system, id)
	if err != nil || !found || (u.Withdrawn() && !withdrawn) {
		return Upload{}, nil, false, err
	}
	contents, err = readContents(conn, u.SHA256)
	return u, contents, true, err
}

// WithdrawSystemUpload withdraws the system's own file with the given ID,
// as the administrator, and returns its record. It fails with
// ErrUnknownUpload if the system has no such file. Withdrawing a withdrawn
// file keeps the time it was first withdrawn. Nothing is deleted.
func (s *Store) WithdrawSystemUpload(ctx context.Context, system string, id int64, now time.Time) (u Upload, err error) {
	conn, err := s.take(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer s.pool.Put(conn)
	defer sqlitex.Save(conn)(&err)
	err = sqlitex.Execute(conn, `
UPDATE uploads SET withdrawn_at = coalesce(withdrawn_at, ?1)
WHERE id = ?2 AND game_id IS NULL AND system_id = (SELECT id FROM systems WHERE code = ?3);`, &sqlitex.ExecOptions{
		Args: []any{now.UTC().Format(timeLayout), id, system},
	})
	if err != nil {
		return Upload{}, err
	}
	u, found, err := systemUpload(conn, system, id)
	if err != nil {
		return Upload{}, err
	} else if !found {
		return Upload{}, ErrUnknownUpload
	}
	return u, nil
}
