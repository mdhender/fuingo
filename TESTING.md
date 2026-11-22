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

## Accounts

Every email has one row in the `accounts` table, whether it plays, manages
games, or both.

- **Players** are rows in the `game_players` table: an account and a game.
  A player's ID is the player's number. An account has no password until
  its owner chooses one through a mailed link.
- **GMs** are rows in the `game_managers` table: an account and a game.
- **Passwords** are the account's `hashed_secret`. Only an account with one
  can log in to `/api/`, with `POST /api/login`, which returns a bearer token
  for the `Authorization` header.

`fuingo account create`, `fuingo game add --manager`, and `fuingo game add
--player` add to the tables. No command sets a password.

## Mail in tests

Tests never reach Mailgun. Give the code under test a
`mailtest.Recorder` (`internal/mail/mailtest`) as its `mail.Sender`, and
read the link out of a recorded message with `mailtest.Link`. `cmd/fuingo`
builds its sender through the `newSender` variable so its tests can do the
same. The Mailgun client itself is tested against an `httptest.Server`.

The `.env.development.local` and `.env.agent.local` files may hold a Mailgun
sandbox, whose `FUINGO_MAILGUN_SANDBOX_TO_ADDR` is the only address it can
reach. Running the programs in those environments sends real mail there.

## Default accounts

Every new database (any in-memory database or any persistent database made by `fuingodb create` in `development` or `agent`) starts with a default system, games, players, and accounts.

- These passwords are fixed, public test values, not secrets.
  They are the only credentials allowed in source control; see [AGENTS.md](AGENTS.md).
  They are defined in `internal/devdata`; keep these tables in sync with it.
- Never create them in any other environment.
- Never re-seed an existing database.
  Changed or deleted default accounts stay that way; recreate the database to restore them.
- Passwords are hashed with `bcrypt.MinCost`, like any other account's.

### Default system and games

The system is `system01`. Both games are in it, and both are active. The
tables below refer to games by code; their IDs are whatever the database
assigns.

| System | Code |
| --- | --- |
| `system01` | `game01` |
| `system01` | `game02` |

### Default players

All are active. Their accounts have no password.

| Game | Email |
| --- | --- |
| `game01` | `catbird@example.com` |
| `game01` | `spoonbill@example.com` |
| `game02` | `caracara@example.com` |

### Default accounts with passwords

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
exceptions to account creation. Otherwise, accounts are created only by the
`cmd/fuingo` administrator's CLI (`fuingo account create` and `fuingo game add
--player`) and by a GM adding a player through `/api/`.

### Create an API account

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

### Log in an API account

```text
POST /__development/log-me-in/<email>
POST /__agent/log-me-in/<email>
```

- Encode the email as a URL path segment.
- Require an existing account with a password; return an error rather than
  creating one. Refuse an account without a password, and a disabled one.
- Issue the same authorization token that a normal `/api/` login issues for
  that account, and return it in the response for use in the `Authorization`
  header, in the same JSON as `POST /api/login`. Do not set cookies or
  redirect. Never log the token.

## Verification contract

When changing the default accounts or in-memory databases, test that they exist
in `development` and `agent` and are refused everywhere else.

When changing the HTTP helpers, cover environment and prefix isolation,
non-POST requests, duplicate account creation without mutation, and CSRF
rejection through HTTP tests. Test that `log-me-in` refuses missing accounts,
disabled accounts, and accounts without a password, and that its token
authenticates `/api/` requests.
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
