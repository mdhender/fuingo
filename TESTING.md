# Testing environments and accounts

Read this before using or changing the default test accounts, in-memory
databases, or the HTTP account helpers. It covers what exists only in the
`development` and `agent` environments, and which kind of database a Go test
should use. For `go vet` and `go test`, see
[WORKFLOW.md](WORKFLOW.md); for public origins, portals, secure cookies, and
CSRF protection, follow [PROXIES.md](PROXIES.md).

## Environments

Resolve the application environment from `FUINGO_ENV`, with an explicitly
supplied `--env` flag taking precedence. Everything in this document is
available only in `development` and `agent`; `test`, `production`, and every
other environment must never provide it. Do not enable any of it based on
request headers, hostname, or a query parameter.

## Accounts and credentials

Every email has one row in the `accounts` table, whether it uploads, manages
games, or both. It can hold two kinds of credential, and neither can act as
the other:

- **Upload credentials** are rows in the `game_uploaders` table: an account,
  a game, and a token. They authorize uploads through the web form and never
  authenticate to `/api/`. An account made for an uploader has no password.
- **API passwords** are the account's `hashed_secret`. Only an account with
  one can authenticate to `/api/`, with the `Authorization` header; the
  password never authorizes an upload. `game_managers` lists the games each
  account manages.

The tables and their default rows exist, and `fuingo account create`,
`fuingo account set-password`, and `fuingo game add --manager` add to them,
but nothing authenticates to the API until the future `/api/` phase described
in [AGENTS.md](AGENTS.md). Do not implement the API account helpers below
until that phase starts.

## Default accounts

Every new database (any in-memory database or any persistent database made by `fuingodb create` in `development` or `agent`) starts with a set of default games and accounts.

- These tokens and passwords are fixed, public test values, not secrets.
  They are the only credentials allowed in source control; see [AGENTS.md](AGENTS.md).
  They are defined in `internal/devdata`; keep these tables in sync with it.
- Never create them in any other environment.
- Never re-seed an existing database.
  Changed or deleted default accounts stay that way; recreate the database to restore them.
- Upload tokens are stored as credential hashes keyed with `FUINGO_PEPPER`, like
  any other upload account, so seeding requires the pepper. A database seeded
  with one pepper does not accept the default tokens under another.
- API passwords are hashed with `bcrypt.MinCost`, like any other API account.

### Default games

Both are active. The tables below refer to games by code; their IDs are
whatever the database assigns.

| Code |
| --- |
| `game01` |
| `game02` |

### Default upload accounts

All are active.

| Game | Email | Token |
| --- | --- | --- |
| `game01` | `catbird@example.com` | `8eb87beb-0869-4815-8ce8-afeef14da57e` |
| `game01` | `spoonbill@example.com` | `942a563c-c911-4c84-848b-fe4e96122d8c` |
| `game02` | `caracara@example.com` | `12fc952a-2b17-49e5-ba31-329a3ba474e4` |

### Default API accounts

All are active. The Games column lists the games each account manages.

| Email | Password | Games |
| --- | --- | --- |
| `acct01@example.com` | `worser-choices-2001` | `game01` |
| `acct02@example.com` | `worser-choices-2002` | `game02` |

## In-memory databases

`fuingod --db :memory:` runs against a fresh, private in-memory database that
is discarded when the server stops. Reject `:memory:` outside `development` and
`agent`. Every `fuingodb` command always requires a database file, because an
in-memory database would vanish when it exits.

## HTTP helpers

### Prefixes

Register the helpers only under the prefix for the resolved environment:

| Environment                                   | Helper prefix |
| --------------------------------------------- | --- |
| `development`                                 | `/__development` |
| `agent`                                       | `/__agent` |
| All others, including `test` and `production` | None |

Do not register `/__development` in `agent` or `/__agent` in `development`. Unavailable
helper routes must return 404, not redirect to an available prefix.

Every helper requires **POST**. GET, HEAD, and OPTIONS must not create accounts
or authorization tokens. Keep normal CSRF protection enabled; these helpers are
not CSRF bypasses.

These helpers and the default accounts are the explicit development/agent-only
exceptions to account creation. Otherwise, accounts and credentials are
created only by the `cmd/fuingo` operators' CLI: `fuingo game add --uploader`
and `fuingo account create`.

### Create an upload account

```text
POST /__development/create-account/<game>/<email>/<token>
POST /__agent/create-account/<game>/<email>/<token>
```

- `<game>` is a game code, lowercased like the email. Encode the game, email,
  and token as URL path segments.
- Require an existing game; return an error rather than creating one.
- Insert an active row in `game_uploaders` for that game and the account with
  the lowercased email, creating the account without a password if needed,
  with the `hashed_credentials` described in [AGENTS.md](AGENTS.md). Never log
  the token.
- Return an error if the game and email already have a row, or if the
  `hashed_credentials` are already in use. Because the hash includes the game's ID,
  the same email and token may be created in another game.
  Never modify an existing row, including when creation requests race.

### Create an API account (future `/api/` phase)

```text
POST /__development/create-api-account/<email>
POST /__agent/create-api-account/<email>
```

- Encode the email as a URL path segment.
- Create the account with a randomly generated password and no managed games,
  and return the password in the response.
  Hash it with bcrypt using `bcrypt.MinCost`, as required by [AGENTS.md](AGENTS.md).
  Never log the plaintext password.
- Return an error if the account already exists.
  Never recreate it, reset its password, or otherwise modify it, including when creation requests race.

### Log in an API account (future `/api/` phase)

```text
POST /__development/log-me-in/<email>
POST /__agent/log-me-in/<email>
```

- Encode the email as a URL path segment.
- Require an existing account with a password; return an error rather than
  creating one. Refuse an account without a password.
- Issue the same authorization token that a normal `/api/` login issues for
  that account, and return it in the response for use in the `Authorization`
  header. Do not set cookies or redirect. Never log the token.

## Verification contract

When changing the default accounts or in-memory databases, test that they exist
in `development` and `agent` and are refused everywhere else.

When changing the HTTP helpers, cover environment and prefix isolation,
non-POST requests, duplicate account creation without mutation, and CSRF
rejection through HTTP tests. Test that a helper-created upload account can
upload through the web form, and that `create-account` refuses an unknown game
code. Once the `/api/` phase exists, test that
`log-me-in` refuses missing accounts and accounts without a password, that its token authenticates
`/api/` requests, and that each kind of account is refused by the other's
routes. Exercise an upload with a
helper-created account through the public HTTPS origin in a browser as well.
Keep helpers absent in `test`; HTTP tests can explicitly configure `development`
or `agent` when testing them.

## Databases in Go tests

Go tests use two kinds of database: private in-memory databases and files in
`t.TempDir()`. Choose by what the test calls.

- **Store, server, and other package tests** that hold a `*store.Store` for the
  whole test use `store.OpenPrivateMemory`, named after the test. They are fast,
  and parallel tests stay isolated.
- **Command tests** (`cmd/fuingodb`, `cmd/fuingo`) call the command functions with the same
  `--env` and `--db` flags a user types, so they need a database file in
  `t.TempDir()`:
  - `fuingodb` and `fuingo` reject `--db :memory:`.
  - Each command opens and closes its own connection. A private in-memory
    database would disappear when that connection closes. A file survives
    between the test's setup, the command, and the test's checks.
  - The file goes through the same path a user's does: flag parsing,
    `store.Open`'s ownership and schema checks, and the hint to run
    `fuingodb migrate`.
- **Tests of the file contract** (Create, Open, Migrate, ownership, and WAL)
  use files in `t.TempDir()`, because an in-memory database cannot use WAL. See
  [SQLITE3.md](SQLITE3.md).

Keep command functions as wrappers around the flags and the database file, and
test them end to end against a temporary file. Do not have them take an open
`*store.Store` so they can be tested in memory: the flags and file handling
would then go untested.
