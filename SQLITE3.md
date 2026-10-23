# SQLite guidance for agents

Follow these requirements when implementing database code. This document defines
the contract; it does not introduce a database implementation or dependencies.

## Libraries

- Use `zombiezen.com/go/sqlite`, backed by the pure-Go `modernc.org/sqlite`
  implementation. Use ZombieZen's connection API, not a separate `database/sql`
  driver or a CGO-based SQLite implementation.
- Use `zombiezen.com/go/sqlite/sqlitex` for SQL helpers and connection pools.
- Use `zombiezen.com/go/sqlite/sqlitemigration` for migrations. Keep ordered
  migration scripts in `sqlitemigration.Schema`; do not build a second migration
  framework. Set `Schema.AppID` to the application ID below and let ZombieZen
  manage `PRAGMA user_version`. Do not edit already-applied migrations.

## Squashing migrations

While the version in `version.go` has `PreRelease: "alpha"`, the user may ask to
squash migrations by folding them into migration 1. This is the only exception
to the rule against editing applied migrations, and it applies only when the
user asks. In beta or later, refuse to squash and append a new migration instead.

A squash keeps the schema version unchanged, so existing databases still appear
current but do not have the new schema. `fuingodb migrate` cannot repair them.
Recreate each one with `fuingodb create`, after backing it up or getting the
user's agreement to discard it.

## Ownership

The database application ID is the ASCII bytes `fuin`, interpreted as a big-endian
32-bit integer:

```go
const applicationID int32 = 0x6675696E // 1718970734, ASCII "fuin"
```

- Store this value in `PRAGMA application_id` when creating a new database.
- When opening an existing database, read and require this exact value before
  running migrations or any mutating initialization, including enabling WAL.
  Reject ID `0`, every other ID, and files that are not valid SQLite databases.
- Never adopt, stamp, migrate, or otherwise repair a foreign database merely
  because it has no tables or an unset ID. Report a clear ownership error.
- `Schema.AppID` alone is not a sufficient guard: ZombieZen's migration pool can
  adopt an empty database whose ID is `0`. It also acquires write intent before
  checking ownership. Perform the strict ownership check first.
- Check ownership on each pool connection before that connection's mutating
  initialization. Do not expose connections until initialization succeeds.

## Separate creation and opening

Provide distinct public methods for **Create**, **Migrate**, and **Open**,
rather than an open-or-create method or a boolean that silently changes the
operation. Only the `fuingodb` command creates or migrates a database; the
`fuingod` server only opens one.

### Create a persistent database

- Require a new file. Fail if the destination already exists, even if it is empty
  or already belongs to this application. Reserve the file exclusively (for
  example, with `os.O_CREATE | os.O_EXCL`) rather than using a check-then-create
  sequence. SQLite's `OpenCreate` flag alone does not reject existing files.
- Require the parent directory to exist. Never call `os.Mkdir` or `os.MkdirAll`
  to create a missing database directory. Return the filesystem error instead.
- Initialize only the file reserved by this operation: set the application ID,
  configure connection invariants, and apply migrations before returning it.
- If creation fails, close all connections and clean up only files created by
  this operation. Never remove or overwrite a pre-existing database.

### Open a persistent database

- Require an existing file and an existing parent directory; never create either.
- Use explicit `sqlite.OpenReadWrite` flags without `sqlite.OpenCreate` or
  `sqlite.OpenWAL` for the initial ownership check. Do not use zero/default flags.
- After ownership is accepted, require the current schema version and configure
  connection invariants. Open must never apply migrations: reject an older
  schema as out of date, and reject a newer one rather than attempting a
  downgrade.

### Migrate a persistent database

- Apply the same no-create and ownership checks as Open before anything can
  modify the file.
- Use ZombieZen migrations to bring an older supported schema up to date.
  Reject unsupported newer schemas rather than attempting a downgrade.
- Preserve the no-create and ownership rules when constructing the pool, not
  just in an initial probe connection. Prefer literal filesystem paths; do not
  allow arbitrary SQLite URI parameters to bypass persistent Create/Open rules.

## Connection pools

- Always supply explicit flags. ZombieZen's zero/default flags include
  `OpenCreate`, `OpenWAL`, and `OpenURI`. `OpenWAL` changes journal mode inside
  `OpenConn`, before application code can validate ownership. Supplying any
  nonzero flags replaces the defaults; include the access mode explicitly.
- Use `PrepareConn` to enforce the invariants on **every** pooled connection,
  including the migration connection, outside a transaction:
  1. Validate the application ID.
  2. For persistent databases, execute `PRAGMA journal_mode=WAL` and check that
     the returned mode is `wal`; fail if WAL cannot be established.
  3. Execute `PRAGMA foreign_keys=ON` and read it back to require `1`.
- WAL is persistent database state; foreign-key enforcement is connection-local.
  Configuring only one connection is insufficient. Do not rely on `OpenWAL`
  succeeding as proof that WAL is active: it does not check the returned mode.
- `PrepareConn` runs lazily on the first checkout of each connection, not at pool
  construction, and must be concurrency-safe. In a migration pool it runs on the
  migration connection before migrations, so it must not require migrated tables.
  It is not a reset hook for every checkout; callers must not disable invariants.
- `sqlitemigration.NewPool` initializes asynchronously. Force initialization by
  taking a connection and checking the error before returning a ready database
  handle. Return borrowed connections with `Put` and close pools during cleanup.
- Prefer migrations that work with foreign keys enabled. If a migration needs
  ZombieZen's `DisableForeignKeys` support, explicitly check referential integrity
  and ensure enforcement is restored before the connection reaches callers.

## In-memory databases

Support both shared and private in-memory databases explicitly, separate from
persistent filesystem paths. Both modes must initialize the application ID, run
the same migrations, and enforce foreign keys on every connection.

### Shared memory

- Use a named URI such as `file:unique-test-name?mode=memory&cache=shared` with
  explicit `sqlite.OpenReadWrite | sqlite.OpenCreate | sqlite.OpenURI` flags.
- Connections to the same name in the same process share one database. Give each
  independent test a unique name so parallel tests cannot share state accidentally.
- Keep at least one connection alive for the database's intended lifetime. The
  database disappears when the last connection closes; closing and reopening a
  pool does not preserve it without an anchor connection.
- Initialize ownership on the newly created memory database before using a strict
  pool ownership guard. Reusing a live shared database must validate its ID, not
  overwrite it. Preserve an anchor across initialization and pool creation.

### Private memory

- A direct `sqlite.OpenConn(":memory:", sqlite.OpenReadWrite)` creates a private
  database, but ZombieZen's pool rejects the literal `:memory:` name, even when
  `PoolSize` is `1`.
- To use the pool API, use a URI such as
  `file:unique-test-name?mode=memory&cache=private`, explicit
  `sqlite.OpenReadWrite | sqlite.OpenURI` flags, and **PoolSize: 1**.
  Additional connections would each have a different database, not another
  connection to the migrated schema.
- Initialize a private memory database on its sole connection; a separate probe
  or bootstrap connection would initialize a different database. Any ID-`0`
  initialization exception must be limited to this explicit fresh-memory creation
  path, never persistent Open or reuse of an existing database.

### WAL exception

SQLite in-memory databases cannot use WAL: their journal mode is `memory` (or
`off`). Do not pretend that requesting WAL enables it. For memory databases, use
and verify `journal_mode=MEMORY`, while still requiring foreign keys. Persistent
pools must enforce WAL without this exception. Use temporary on-disk databases
when testing actual WAL behavior or concurrency that depends on WAL.

## Verification expectations

When implementing database support, test the observable contract: successful
Create, Migrate, and Open; Open's refusal to migrate an outdated schema;
rejection of missing files, missing parents, existing Create targets, foreign IDs, ID `0`, and invalid files without adopting or
modifying them; connection-wide WAL and foreign-key enforcement; private-memory
isolation; named shared-memory sharing and lifetime; and parallel-test isolation.
Use multiple simultaneously borrowed connections to verify pool-wide invariants.

## References

- [ZombieZen SQLite](https://pkg.go.dev/zombiezen.com/go/sqlite)
- [Pool options](https://pkg.go.dev/zombiezen.com/go/sqlite/sqlitex#PoolOptions)
- [ZombieZen migrations](https://pkg.go.dev/zombiezen.com/go/sqlite/sqlitemigration)
- [SQLite application ID](https://www.sqlite.org/pragma.html#pragma_application_id)
- [SQLite in-memory databases](https://www.sqlite.org/inmemorydb.html)
- [SQLite journal modes](https://www.sqlite.org/pragma.html#pragma_journal_mode)
