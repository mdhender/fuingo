// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sqlitedb implements the SQLite file contract in SQLITE3.md for
// any application's database: Create, Migrate, and Open are separate
// operations, only Create makes a new file, only Create and Migrate apply
// migrations, and every operation refuses a file that does not carry the
// schema's application ID. Each connection enforces WAL (or MEMORY for
// in-memory databases) and foreign keys.
package sqlitedb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdhender/fuingo/internal/cerrs"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitemigration"
	"zombiezen.com/go/sqlite/sqlitex"
)

const (
	ErrDatabaseExists = cerrs.Error("database already exists")
	ErrInvalidName    = cerrs.Error("invalid database name")
	ErrNewerSchema    = cerrs.Error("database schema is newer than this program supports")
	ErrNotOwned       = cerrs.Error("database belongs to another application")
	ErrNotRegular     = cerrs.Error("database is not a regular file")
	ErrOutdatedSchema = cerrs.Error("database schema is out of date")
)

const (
	// PersistentPoolSize is the number of connections to a database file.
	PersistentPoolSize = 8
	busyTimeout        = 5 * time.Second
)

// Schema describes an application's database.
type Schema struct {
	// AppID is stored in PRAGMA application_id and required on open.
	AppID int32
	// Owner names the application in errors, such as "fuingo".
	Owner string
	// MigrateHint tells the user how to migrate an outdated database, such
	// as "run fuingodb migrate".
	MigrateHint string
	// Migrations are applied in order; never edit one that has been applied.
	Migrations []string
}

func (s Schema) migration() sqlitemigration.Schema {
	return sqlitemigration.Schema{AppID: s.AppID, Migrations: s.Migrations}
}

// CurrentVersion is the schema version this program expects.
func (s Schema) CurrentVersion() int {
	return len(s.Migrations)
}

// DB is a handle to an initialized database. It is safe for concurrent use.
type DB struct {
	Pool *sqlitex.Pool
	// anchor keeps a shared in-memory database alive; nil otherwise.
	anchor *sqlite.Conn
}

// Close closes every connection held by the database.
func (d *DB) Close() error {
	err := d.Pool.Close()
	if d.anchor != nil {
		err = errors.Join(err, d.anchor.Close())
	}
	return err
}

// SchemaVersion returns the database's schema version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	conn, err := d.Pool.Take(ctx)
	if err != nil {
		return 0, err
	}
	defer d.Pool.Put(conn)
	return UserVersion(conn)
}

// Flags for persistent files. They never include OpenCreate, OpenWAL, or
// OpenURI: the file must exist, WAL is enabled only after the ownership check,
// and the path is always a literal filesystem path.
const persistentFlags = sqlite.OpenReadWrite

// Create makes a new database at path, initializes it, applies every
// migration, and returns it opened. The file must not already exist and its
// parent directory must. On failure, Create removes only the files it made.
func (s Schema) Create(ctx context.Context, path string) (_ *DB, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// A leftover journal would be applied to the new file; refuse rather than
	// adopt it, and so never remove a file this call did not create.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return nil, fmt.Errorf("create %s: %w: %s exists", path, ErrDatabaseExists, path+suffix)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create %s: %w", path, ErrDatabaseExists)
	} else if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			for _, name := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
				_ = os.Remove(name)
			}
		}
	}()

	if err := s.initializeFile(ctx, path); err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	return s.Open(ctx, path)
}

// initializeFile stamps and migrates the empty file that Create reserved.
func (s Schema) initializeFile(ctx context.Context, path string) error {
	conn, err := sqlite.OpenConn(path, persistentFlags)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := s.setApplicationID(conn); err != nil {
		return err
	}
	if err := s.prepareConn("wal")(conn); err != nil {
		return err
	}
	return sqlitemigration.Migrate(ctx, conn, s.migration())
}

// Open opens an existing database that is already at the current schema.
// It never creates, stamps, or migrates the file.
func (s Schema) Open(ctx context.Context, path string) (*DB, error) {
	path, err := checkExistingFile(path)
	if err != nil {
		return nil, err
	}
	if err := probe(path, func(conn *sqlite.Conn) error {
		if err := s.checkApplicationID(conn); err != nil {
			return err
		}
		return s.checkCurrentSchema(conn)
	}); err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	pool, err := sqlitex.NewPool(path, sqlitex.PoolOptions{
		Flags:       persistentFlags,
		PoolSize:    PersistentPoolSize,
		PrepareConn: s.prepareConn("wal"),
	})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return ready(ctx, &DB{Pool: pool})
}

// Migrate brings an existing database up to the current schema.
// It applies the same existence and ownership checks as Open before
// anything can modify the file.
func (s Schema) Migrate(ctx context.Context, path string) error {
	path, err := checkExistingFile(path)
	if err != nil {
		return err
	}
	conn, err := sqlite.OpenConn(path, persistentFlags)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", path, err)
	}
	defer conn.Close()
	// prepareConn checks ownership before enabling WAL.
	if err := s.prepareConn("wal")(conn); err != nil {
		return fmt.Errorf("migrate %s: %w", path, err)
	}
	if version, err := UserVersion(conn); err != nil {
		return fmt.Errorf("migrate %s: %w", path, err)
	} else if version > len(s.Migrations) {
		return fmt.Errorf("migrate %s: %w", path, ErrNewerSchema)
	}
	if err := sqlitemigration.Migrate(ctx, conn, s.migration()); err != nil {
		return fmt.Errorf("migrate %s: %w", path, err)
	}
	return nil
}

// OpenPrivateMemory creates a fresh, private in-memory database.
// It has a single connection, because each connection to a private memory
// database would see a different database.
func (s Schema) OpenPrivateMemory(ctx context.Context, name string) (*DB, error) {
	uri, err := MemoryURI(name, "private")
	if err != nil {
		return nil, err
	}
	pool, err := sqlitex.NewPool(uri, sqlitex.PoolOptions{
		Flags:    sqlite.OpenReadWrite | sqlite.OpenURI,
		PoolSize: 1,
		PrepareConn: func(conn *sqlite.Conn) error {
			// A private memory database is always new on its sole connection,
			// so this is the one place an ID of 0 may be stamped.
			if err := s.setApplicationID(conn); err != nil {
				return err
			}
			if err := s.prepareConn("memory")(conn); err != nil {
				return err
			}
			return sqlitemigration.Migrate(context.Background(), conn, s.migration())
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open private memory %q: %w", name, err)
	}
	return ready(ctx, &DB{Pool: pool})
}

// CreateSharedMemory creates a named in-memory database shared by every
// connection in this process. It fails if a database with that name is
// already live. The database lasts until the returned store is closed.
func (s Schema) CreateSharedMemory(ctx context.Context, name string) (*DB, error) {
	return s.sharedMemory(ctx, name, true)
}

// OpenSharedMemory opens a live named in-memory database made by
// CreateSharedMemory. The database lasts until every store using it is closed.
func (s Schema) OpenSharedMemory(ctx context.Context, name string) (*DB, error) {
	return s.sharedMemory(ctx, name, false)
}

func (s Schema) sharedMemory(ctx context.Context, name string, create bool) (_ *DB, err error) {
	uri, err := MemoryURI(name, "shared")
	if err != nil {
		return nil, err
	}
	const flags = sqlite.OpenReadWrite | sqlite.OpenCreate | sqlite.OpenURI
	anchor, err := sqlite.OpenConn(uri, flags)
	if err != nil {
		return nil, fmt.Errorf("open shared memory %q: %w", name, err)
	}
	defer func() {
		if err != nil {
			anchor.Close()
			err = fmt.Errorf("open shared memory %q: %w", name, err)
		}
	}()
	anchor.SetBusyTimeout(busyTimeout)
	if create {
		if err := s.claimSharedMemory(anchor); err != nil {
			return nil, err
		}
	}
	if err := s.prepareConn("memory")(anchor); err != nil {
		return nil, err
	}
	if create {
		if err := sqlitemigration.Migrate(ctx, anchor, s.migration()); err != nil {
			return nil, err
		}
	} else if err := s.checkCurrentSchema(anchor); err != nil {
		return nil, err
	}
	pool, err := sqlitex.NewPool(uri, sqlitex.PoolOptions{
		Flags:       flags,
		PrepareConn: s.prepareConn("memory"),
	})
	if err != nil {
		return nil, err
	}
	return ready(ctx, &DB{Pool: pool, anchor: anchor})
}

// claimSharedMemory stamps a shared memory database that is new, failing if
// it already holds anything. The transaction keeps concurrent claims of the
// same name from both succeeding.
func (s Schema) claimSharedMemory(conn *sqlite.Conn) (err error) {
	defer sqlitex.Save(conn)(&err)
	id, err := ReadApplicationID(conn)
	if err != nil {
		return err
	}
	var objects int
	if err := sqlitex.ExecuteTransient(conn, "SELECT count(*) FROM sqlite_schema;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			objects = stmt.ColumnInt(0)
			return nil
		},
	}); err != nil {
		return err
	}
	if id != 0 || objects != 0 {
		return ErrDatabaseExists
	}
	return s.setApplicationID(conn)
}

// ready forces every lazy initialization step to run on one connection and
// reports its error, so callers never receive a store that cannot be used.
func ready(ctx context.Context, d *DB) (*DB, error) {
	conn, err := d.Pool.Take(ctx)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	d.Pool.Put(conn)
	return d, nil
}

// memoryURI builds a URI for a named memory database. The name is escaped so
// it cannot add URI parameters.
func MemoryURI(name, cache string) (string, error) {
	if name == "" {
		return "", ErrInvalidName
	}
	return "file:" + url.PathEscape(name) + "?mode=memory&cache=" + cache, nil
}

// checkExistingFile returns the absolute path of an existing regular file.
// Making the path absolute also keeps it from being read as a URI.
func checkExistingFile(path string) (string, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return "", fmt.Errorf("%q: %w", path, ErrInvalidName)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	sb, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !sb.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %w", path, ErrNotRegular)
	}
	return path, nil
}

// probe runs fn on a temporary connection that cannot create the file.
func probe(path string, fn func(*sqlite.Conn) error) error {
	conn, err := sqlite.OpenConn(path, persistentFlags)
	if err != nil {
		return err
	}
	return errors.Join(fn(conn), conn.Close())
}

// prepareConn returns the function that enforces the connection invariants:
// it checks ownership, then sets and verifies the journal mode, then enables
// and verifies foreign-key enforcement.
func (s Schema) prepareConn(journalMode string) sqlitex.ConnPrepareFunc {
	return func(conn *sqlite.Conn) error {
		conn.SetBusyTimeout(busyTimeout)
		if err := s.checkApplicationID(conn); err != nil {
			return err
		}
		mode, err := PragmaText(conn, "PRAGMA journal_mode="+journalMode+";")
		if err != nil {
			return fmt.Errorf("set journal mode: %w", err)
		}
		if !strings.EqualFold(mode, journalMode) {
			return fmt.Errorf("journal mode is %q, want %q", mode, journalMode)
		}
		if err := sqlitex.ExecuteTransient(conn, "PRAGMA foreign_keys=ON;", nil); err != nil {
			return fmt.Errorf("enable foreign keys: %w", err)
		}
		if fk, err := PragmaText(conn, "PRAGMA foreign_keys;"); err != nil {
			return fmt.Errorf("read foreign keys: %w", err)
		} else if fk != "1" {
			return fmt.Errorf("foreign keys are %q, want enabled", fk)
		}
		return nil
	}
}

func (s Schema) checkApplicationID(conn *sqlite.Conn) error {
	id, err := ReadApplicationID(conn)
	if err != nil {
		return err
	}
	if id != s.AppID {
		return fmt.Errorf("%w: application ID is %#x, want %#x (%s)", ErrNotOwned, uint32(id), uint32(s.AppID), s.Owner)
	}
	return nil
}

func (s Schema) checkCurrentSchema(conn *sqlite.Conn) error {
	version, err := UserVersion(conn)
	if err != nil {
		return err
	}
	switch want := len(s.Migrations); {
	case version < want:
		return fmt.Errorf("%w: version %d, want %d; %s", ErrOutdatedSchema, version, want, s.MigrateHint)
	case version > want:
		return fmt.Errorf("%w: version %d, want %d", ErrNewerSchema, version, want)
	}
	return nil
}

func ReadApplicationID(conn *sqlite.Conn) (int32, error) {
	var id int32
	err := sqlitex.ExecuteTransient(conn, "PRAGMA application_id;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			id = stmt.ColumnInt32(0)
			return nil
		},
	})
	if err != nil {
		return 0, fmt.Errorf("read application ID: %w", err)
	}
	return id, nil
}

func (s Schema) setApplicationID(conn *sqlite.Conn) error {
	if err := sqlitex.ExecuteTransient(conn, fmt.Sprintf("PRAGMA application_id = %d;", s.AppID), nil); err != nil {
		return fmt.Errorf("set application ID: %w", err)
	}
	return nil
}

func UserVersion(conn *sqlite.Conn) (int, error) {
	var version int
	err := sqlitex.ExecuteTransient(conn, "PRAGMA user_version;", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			version = stmt.ColumnInt(0)
			return nil
		},
	})
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func PragmaText(conn *sqlite.Conn, query string) (string, error) {
	var text string
	err := sqlitex.ExecuteTransient(conn, query, &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			text = stmt.ColumnText(0)
			return nil
		},
	})
	return text, err
}
