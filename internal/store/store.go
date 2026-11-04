// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store wraps the SQLite database.
//
// Create, Migrate, and Open are separate operations: only Create makes a new
// file, only Create and Migrate apply migrations, and every operation refuses
// a file that does not carry this application's ID. See SQLITE3.md; the file
// contract itself is implemented by package sqlitedb.
package store

import (
	"context"

	"github.com/mdhender/fuingo/internal/sqlitedb"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// applicationID is the ASCII bytes "fuin" as a big-endian 32-bit integer.
const applicationID int32 = 0x6675696E

const (
	ErrDatabaseExists = sqlitedb.ErrDatabaseExists
	ErrInvalidName    = sqlitedb.ErrInvalidName
	ErrNewerSchema    = sqlitedb.ErrNewerSchema
	ErrNotOwned       = sqlitedb.ErrNotOwned
	ErrNotRegular     = sqlitedb.ErrNotRegular
	ErrOutdatedSchema = sqlitedb.ErrOutdatedSchema
)

const persistentPoolSize = sqlitedb.PersistentPoolSize

var schema = sqlitedb.Schema{
	AppID:       applicationID,
	Owner:       "fuingo",
	MigrateHint: "run fuingodb migrate",
	Migrations:  migrations,
}

// Store is a handle to an initialized database.
// It is safe for concurrent use.
type Store struct {
	db   *sqlitedb.DB
	pool *sqlitex.Pool
}

func wrap(db *sqlitedb.DB, err error) (*Store, error) {
	if err != nil {
		return nil, err
	}
	return &Store{db: db, pool: db.Pool}, nil
}

// Close closes every connection held by the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// take borrows a connection; callers must return it with s.pool.Put.
func (s *Store) take(ctx context.Context) (*sqlite.Conn, error) {
	return s.pool.Take(ctx)
}

// Create makes a new database at path, initializes it, applies every
// migration, and returns it opened. The file must not already exist and its
// parent directory must. On failure, Create removes only the files it made.
func Create(ctx context.Context, path string) (*Store, error) {
	return wrap(schema.Create(ctx, path))
}

// Open opens an existing database that is already at the current schema.
// It never creates, stamps, or migrates the file.
func Open(ctx context.Context, path string) (*Store, error) {
	return wrap(schema.Open(ctx, path))
}

// Migrate brings an existing database up to the current schema.
func Migrate(ctx context.Context, path string) error {
	return schema.Migrate(ctx, path)
}

// CurrentSchemaVersion is the schema version this program expects.
func CurrentSchemaVersion() int {
	return schema.CurrentVersion()
}

// SchemaVersion returns the database's schema version. A store returned by
// Open is always at CurrentSchemaVersion.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return s.db.SchemaVersion(ctx)
}

// OpenPrivateMemory creates a fresh, private in-memory database with a
// single connection.
func OpenPrivateMemory(ctx context.Context, name string) (*Store, error) {
	return wrap(schema.OpenPrivateMemory(ctx, name))
}

// CreateSharedMemory creates a named in-memory database shared by every
// connection in this process. It fails if a database with that name is
// already live. The database lasts until the returned store is closed.
func CreateSharedMemory(ctx context.Context, name string) (*Store, error) {
	return wrap(schema.CreateSharedMemory(ctx, name))
}

// OpenSharedMemory opens a live named in-memory database made by
// CreateSharedMemory. The database lasts until every store using it is closed.
func OpenSharedMemory(ctx context.Context, name string) (*Store, error) {
	return wrap(schema.OpenSharedMemory(ctx, name))
}

// Helpers the tests use.
var (
	memoryURI         = sqlitedb.MemoryURI
	pragmaText        = sqlitedb.PragmaText
	readApplicationID = sqlitedb.ReadApplicationID
	userVersion       = sqlitedb.UserVersion
)
