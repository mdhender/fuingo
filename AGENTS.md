# Agent guidance

## Project direction

Build a Go web server that accepts turn-file uploads for play-by-email (PBeM)
games. It replaces the custom e-mail client code in the existing games: players
upload files over HTTPS instead of mailing them.

- Uploads come from unauthenticated clients. The only authorization is the game
  code, email address, and upload token on the turn file's header line,
  checked against the `game_uploaders` table.
- Players cannot view, list, download, replace, or delete their uploads through
  the web form. This is a deliberate non-feature, not a gap to fill. Listings and
  management belong to the future `/api/` phase described below.
- Distinguish agreed requirements from proposals. Resolve consequential design
  questions with the user rather than inventing behavior.

## Sources and repository layout

- Read and follow [WORKFLOW.md](WORKFLOW.md) before working. It owns the project's
  bug-priority, build/test, versioning, Git, and CI policies; do not duplicate or
  override those policies here.
- Read [PROXIES.md](PROXIES.md) before implementing HTTP startup, listeners,
  redirects, cookies, or CSRF protection. The development origin
  `https://htmx-app.localhost:8443` is shared by all PBeM projects through the
  user's Caddy configuration; keep it.
- Read [SQLITE3.md](SQLITE3.md) before implementing database code.
- Read [TESTING.md](TESTING.md) before changing default accounts, in-memory
  databases, or the development/agent HTTP helpers.
- `go.mod` defines the module `github.com/mdhender/fuingo`.
- `version.go` provides the root package's version API.
- `internal/dotenv` loads environment files; `internal/cerrs` defines constant
  errors. Use both rather than adding alternatives.
- `internal/store` wraps the SQLite database: `Create`, `Migrate`, and `Open`
  for files, plus `OpenPrivateMemory`, `CreateSharedMemory`, and
  `OpenSharedMemory`.
- `internal/credential` computes upload credential hashes; `internal/devdata`
  holds the default games, accounts, uploaders, and managers; `internal/cli` holds the flag
  and environment-file parsing shared by the commands.
- `internal/upload` is the upload handler, implementing the requirements
  below. `internal/clientip` finds the client's address behind trusted
  proxies; `internal/ratelimit` limits failed authorizations per client.
- `internal/server` assembles the browser-facing routes (the page at `/`,
  the vendored HTMX under `/static/`, `POST /upload`, and the
  development/agent helper) behind `http.NewCrossOriginProtection`. HTMX
  2.0.11 is vendored as `internal/server/static/htmx-2.0.11.min.js`.
- `contrib/systemd/fuingod.service` runs `fuingod` as a systemd service;
  README.md documents its setup. Keep the two in sync, and keep the unit's
  `TimeoutStopSec` longer than `fuingod`'s shutdown timeout.
- `cmd/fuingod`, `cmd/fuingodb`, and `cmd/fuingo` exist; [README.md](README.md)
  documents their flags and environment variables. Keep it current when adding
  any of them. The `/api/` routes do not exist yet; do not document them as
  implemented until they are.

## Commands

Use `github.com/peterbourgon/ff/v4` for flags and `internal/dotenv` for
environment files, through `internal/cli`. Explicit flags override environment
values, which override defaults. Every flag `--name` has an environment
variable `FUINGO_NAME`; secrets such as `FUINGO_PEPPER` have no flag. The one
secret with a flag is `fuingo game add --token`, which is command-line only
(below) so that no environment file can give every uploader one token.

The exception is a flag that changes what a command does, or names what it
acts on, rather than configuring it. `fuingo version --schema` prints the
database's schema version instead of the program's version; `--token` and
`--generate-password` choose a new credential; `game add --manager`,
`--uploader`, and `--code` name the account and game to change. Such a flag is honored only on
the command line, never from the environment: set in an environment file, it
would silently change the command for every later run. Pass its name to
`cli.Parse`, which removes its environment variable before applying the
environment, and document it in the command's help and package comment.

Each program owns one area. Put a new command in the program that owns what
it manages, even when another program could reach the same thing:

- **`cmd/fuingo` manages the application data**: games, accounts,
  uploaders, game managers, and uploads. Every command that creates or changes
  rows belongs here, including every `create` verb for application
  data. Outside development and agent, it will be the only way to create
  accounts.
- **`cmd/fuingodb` manages the database itself**: creating, migrating, and any
  future maintenance of the database file and its schema. Never add one of
  these, such as `migrate`, to `fuingo`.
- **`cmd/fuingod` manages the server**: it serves the upload form and owns the
  server's startup flags. It opens an existing database; it never creates or
  migrates one, and it has no subcommands for data.

`fuingo` commands follow the noun-verb pattern, `fuingo <noun> <verb>`, as in
`fuingo game create CODE`: each noun has only the verbs that make sense for it,
and `fuingo <noun> --help` lists them. `--db` is a root flag, as in
`fuingo --db PATH game create CODE`, because almost every command uses the
database. Commands that use it open it with `store.Open` and fail unless it is
at the schema version the program expects. `fuingo` has `version`,
`version --schema` (the database's schema version), `game create CODE`,
`game add --manager EMAIL --code CODE`,
`game add --uploader EMAIL --code CODE [--token TOKEN]`,
`game (disable|enable) [--uploader EMAIL | --manager EMAIL] --code CODE`, and
`account create [--generate-password] EMAIL`,
`account set-password [--generate-password] EMAIL`, and
`account (disable|enable) EMAIL` so far; add further commands only
as the user asks for them.

- Game codes, emails, and tokens given to `fuingo` must be valid header words
  (one or more bytes in `0x21`–`0x7E`), so that an upload can name them. Codes
  and emails are lowercased with `strings.ToLower`.
- `game add --uploader` adds a `game_uploaders` row for the account with that
  email, creating the account without a `hashed_secret` if it does not exist.
  It generates a random UUID token unless `--token` is given, prints a
  generated token once, and never prints a given one. It requires
  `FUINGO_PEPPER`.
- `account create` creates an account with a `hashed_secret`, and fails if
  the email already has an account. `account set-password` sets or replaces
  the `hashed_secret` of an existing account, active or not, including one
  made by `game add --uploader`; it never creates an account. Each reads the
  password from the first line of standard input, without its LF or CRLF, or
  generates one with `--generate-password` and prints it once. Passwords are hashed with bcrypt at `bcrypt.MinCost` and are
  at most 72 bytes.
- `game add` takes exactly one of `--manager` and `--uploader`, and `--token`
  only with `--uploader`. `game add --manager` makes an existing account a
  manager of an existing game, active or not, and refuses an account that
  already manages it, even as a disabled manager.
- `game disable` and `game enable` clear or set `is_active` on the game, or,
  with `--uploader` or `--manager`, on that row of `game_uploaders` or
  `game_managers`. They never delete rows, and repeating one succeeds. They
  take at most one of `--uploader` and `--manager`, and require `--code`.
- `account disable` and `account enable` clear or set `is_active` on an
  account. An inactive account manages no games and uploads to none, whatever
  its `game_managers` and `game_uploaders` rows say.

| Command | Purpose |
| --- | --- |
| `cmd/fuingo` | Application data. The operators' CLI for games, accounts, uploaders, and game managers. |
| `cmd/fuingodb` | The database: `create` and `migrate`. |
| `cmd/fuingod` | The server. Opens an existing database; never creates or migrates one. |

Upload tokens are issued out of band: a game manager emails each uploader the
game code and that game's token. A token cannot be recovered, because only its
hash is stored; a lost token is replaced by resetting it and sending the new
one. Resetting belongs to `cmd/fuingo` or the `/api/` phase.

## Data model

Game codes and account emails can change, so rows are identified by integer
IDs, never by those values. Declare these IDs `INTEGER PRIMARY KEY AUTOINCREMENT`:
without `AUTOINCREMENT`, SQLite may reuse the ID of a deleted row, and IDs must
never be reused.

| Table | Columns | Keys |
| --- | --- | --- |
| `games` | `id`, `code`, `is_active` | PK `id`; `code` unique |
| `accounts` | `id`, `email`, `hashed_secret`, `is_active` | PK `id`; `email` unique |
| `game_uploaders` | `id`, `game_id`, `account_id`, `hashed_credentials`, `is_active` | PK `id`; `(game_id, account_id)` unique; `hashed_credentials` unique; FKs to `games` and `accounts` |
| `game_managers` | `id`, `game_id`, `account_id`, `is_active` | PK `id`; `(game_id, account_id)` unique; FKs to `games` and `accounts` |
| `upload_blobs` | `content_hash`, `contents`, `created_at` | PK `content_hash` |
| `uploads` | `id`, `uploader_id`, `email`, `turn_no`, `content_hash`, `uploaded_at` | PK `id`; FK `uploader_id` → `game_uploaders`; FK `content_hash` → `upload_blobs` |

Two views hold the rules for being active, so queries do not repeat them:
`active_uploaders` lists the `game_uploaders` rows whose row, game, and
account are all active, with the account's email; `active_managers` lists
the `game_managers` rows whose row and account are active, for games active
or not.

- `games.code` is stored lowercased, like emails.
- `accounts` has one row per email, for uploaders and managers alike, with
  the email stored lowercased. `hashed_secret` is the bcrypt hash of the API
  password, or `NULL` for an account that has none; an account without one
  can never authenticate to `/api/`. Whether an account manages a game is
  recorded only in `game_managers`.
- `game_uploaders` holds the upload credentials, one per account and game.
  `hashed_credentials` is the hash described below; the plaintext token is never
  stored. The hash includes the game's ID, so an uploader in several games has
  a separate credential in each, and may even reuse a token across them.
  Because the hash is unique, a match identifies exactly one uploader and game.
  The hash also includes the email, so changing an account's email
  invalidates its upload credentials until their tokens are reset.
- `upload_blobs` holds uploaded file bodies, without the header line, keyed by the lowercase hex
  SHA-256 of the contents. Identical uploads share one blob, across uploaders
  and games, so a blob belongs to no single uploader. `created_at` is when the
  contents first arrived, in UTC.
- `uploads` records each successful upload by its `game_uploaders` row, so its
  history survives a change of the uploader's token. `email` is the
  uploader's lowercased email when the upload arrived, so the history keeps
  it if the account's email later changes. `turn_no` is the turn number from
  the file's header line, stored as text exactly as written. `uploaded_at` is
  UTC.
- Uploaded files are stored only in the database, never on the filesystem.
  Games and other clients read them through the future `/api/` routes, which
  enforce access controls.
- Upload tokens are never stored in `accounts`, and the API password is never
  an upload credential. `game_managers` lists the games each account manages.
- Every foreign key is `ON DELETE RESTRICT`: deleting a row that others refer to
  fails instead of removing history. Revoke access by clearing `is_active`.

Rows in `games`, `game_uploaders`, `accounts`, and `game_managers` come from
`fuingo`, the default data, and the development/agent helpers in
[TESTING.md](TESTING.md). The tables exist in preparation for the `/api/`
phase; the routes that use them do not yet.

## Upload requirements

The web form has one field, the file picker, which accepts `.txt` files and is
enabled from the start. The `accept` filter is only a convenience; the server
enforces the rules below.

Every turn file starts with a header line naming its game, turn, uploader, and
token:

```text
<game code> <turn number> <email address> <token>
```

The server handles each upload in this order:

1. **Size limit.** Reject any upload larger than 64 KiB (65,536 bytes),
   counting the whole file as uploaded. Enforce the limit while reading the
   request (for example, with `http.MaxBytesReader`), not after buffering an
   unbounded body.
2. **Sanity check.** Reject empty files. Skip one leading UTF-8 BOM (`EF BB BF`)
   if present. The next 256 bytes, or all remaining bytes if fewer, must each be
   printable ASCII (`0x20`–`0x7E`), tab (`0x09`), LF (`0x0A`), or CR (`0x0D`). Any
   other control byte, or any byte above `0x7F`, fails the check. Bytes beyond
   that sample are not checked. A file containing only a BOM is empty.
3. **Header.** After the optional BOM, the header line is everything up to the
   first LF, or the whole file if there is none. Trim leading and trailing
   spaces and tabs, and the CR of a CRLF line ending. The trimmed line must be
   exactly four words separated by one or more spaces or tabs, where a word is
   one or more bytes in `0x21`–`0x7E`. The turn number is at most 32 bytes.
   Bare-CR line endings are not supported: such a file has no LF, so its whole
   contents form one invalid header line. The body is everything after the
   first LF; a file whose body is empty is rejected as empty. Header and empty-
   body failures may say what was wrong.
4. **Authorization.** Lowercase the email and the game code from the header
   with `strings.ToLower`; apply no other normalization unless the user agrees
   to it. Look up the game's ID by its code. Compute the credential hash as
   `HMAC-SHA256(pepper, lowercased-email + "\x00" + game-id + "\x00" + token)`,
   where `game-id` is the integer ID in decimal, and look it up in
   `game_uploaders`. Using the ID, not the code, keeps credentials valid when a
   game's code changes. An unknown game code still computes and looks up a
   hash, with ID `0`, and fails like a wrong token. The matching uploader, its
   game, and its account must all be active; an inactive one fails like a
   missing row. There
   are no salts. Compare hashes in constant time.

   Failed authorizations are limited per client address, never per email or
   game, so guessing cannot lock a valid uploader out. Each client may fail 5
   times, then once more per minute. A client over its limit gets `429 Too
   Many Requests` with `Retry-After` instead of an authorization check, even
   with valid credentials. Successful uploads, and the size, sanity, header,
   and empty-body failures before this step, do not count. Reserve a failure
   before the credential lookup and return it on success or server error, so
   concurrent guesses cannot exceed the limit. The client address is the
   connection's peer, unless the peer is a trusted proxy: then it is the
   rightmost `X-Forwarded-For` address that is not a trusted proxy. Trust only
   the proxies configured in `FUINGO_TRUSTED_PROXIES`. Group IPv6 clients by
   their /64 prefix. The limits are kept in memory and reset when the server
   restarts. Track at most 16,384 (16 Ki) clients; a new client beyond that
   replaces the one that has regained the most attempts.
5. **Storage.** Store only the body: the BOM and header line, and so the token,
   never reach the database. Key the body by its lowercase hex SHA-256 in
   `upload_blobs`. If a blob with that hash exists, keep it and do not rewrite
   it.
6. **Record.** Insert an `uploads` row with the matched uploader's game and
   email, the turn number as written in the header, the body's hash, and the
   UTC timestamp. Every successful upload gets its own row, including a repeat
   of content already stored. Do storage and record in one transaction, so a
   blob is never stored without its row.
7. **Response.** Return success or failure to the client. On success, name the
   game, turn, and email, and give the body's size and SHA-256 as a receipt;
   never echo the token. Use one generic message for every authorization
   failure, so a response never reveals whether an email or game exists or
   which credential was wrong. Size, sanity, header, and empty-body failures may
   say what was wrong.

### Secrets

- The pepper comes from `FUINGO_PEPPER`, never from the database, source
  control, or a shared `.env` file. Keep it in the process environment or an
  ignored `.local` file. `fuingod` refuses to start, `fuingodb` refuses to
  create or seed a database, and `fuingo game add --uploader` refuses to
  run, if it is missing or empty.
- Changing the pepper invalidates every stored credential hash. Do not rotate it
  without a plan agreed with the user.
- Never log tokens, passwords, the pepper, or file contents. `fuingo` prints a
  token or password only when it generated it, once, to standard output.
- The default tokens and passwords in [TESTING.md](TESTING.md) are fixed,
  public test values. They are the only credentials allowed in source control.

## User interface

- Build the form with HTMX and Tailwind CSS. Use server-rendered HTML fragments
  for responses; do not introduce a JavaScript framework.
- The form is a single file picker; the uploader's game, turn, email, and
  token come from the file's header line.
- The picker is a drop zone around a visually hidden `<input type="file">`.
  It uploads as soon as one `.txt` file is selected or dropped, with no
  Upload button. Its script rejects more than one file, other extensions,
  empty files, and files over 64 KiB before sending, showing the message in
  the result area, never in a browser dialog. These checks are conveniences;
  the server enforces the rules. The user may later replace it with a
  Tailwind UI kit component.
- Vendor HTMX: commit the minified file for one exact release, with its version
  in the file name, and serve it from the binary with `embed`. Do not load HTMX
  from a CDN.
- Load Tailwind CSS (the `@tailwindcss/browser` build) from a CDN, pinned to an
  exact version. Do not add a Tailwind CLI, watcher, or generated CSS to the
  repository; the user will set those up if the project adopts a Tailwind UI kit
  component.

## Future phase: the `/api/` routes

Do not implement these until the user starts the phase.

- Smarter clients will submit files through `/api/` routes without the web form.
- `/api/` requests authenticate with the `Authorization` header. An inactive
  account fails authentication like a wrong password.
- Only an account with a `hashed_secret` can authenticate to `/api/`. Upload
  tokens never authenticate to `/api/`, and the API password never authorizes
  an upload. Keep the two kinds of credential separate; see
  [TESTING.md](TESTING.md).
- API passwords are hashed with bcrypt at `bcrypt.MinCost`. This is an
  accepted risk; do not raise the cost or propose doing so.
- The API adds listing and management of uploaded files. An account may act
  only on the games it manages in `game_managers`, and only while the account
  and that row are active.

## Verification

- Test observable HTTP and command behavior, not private implementation details.
- Cover the upload contract: the 64 KiB boundary (65,536 bytes accepted, 65,537
  rejected), empty and BOM-only files, a BOM followed by a valid file, every
  boundary byte of the sanity check, files shorter than the 256-byte sample, a
  disallowed byte as the last sampled byte (rejected) and the first byte after
  the sample (accepted), with and without a BOM.
- Cover the header: tabs and multiple spaces as separators, leading and
  trailing spaces and tabs, CRLF and LF endings, three and five words, a
  disallowed byte in a header longer than the sample, bare-CR files, a 32-byte
  turn number (accepted) and a 33-byte one (rejected), a header with no LF, a
  header with an empty body, and that the stored body has no BOM or header.
- Cover authorization: email and game code case folding, wrong email, wrong
  game code, unknown game code, wrong token, the same email and token in two
  games, an inactive uploader, an inactive game, an inactive account, a
  missing pepper, and identical failure responses for unknown emails,
  unknown or wrong game codes, and wrong tokens. Check that format errors are
  reported before authorization and that no response contains the token.
- For duplicate content, cover uploaders in the same game and in different
  games with identical bodies, concurrent uploads, and that the existing blob
  is not rewritten.
- Cover failure limiting: the limit and `Retry-After`, recovery over time,
  that successes and format errors do not count, that valid credentials are
  refused while limited, that other clients (including the same email from
  another address) are unaffected, concurrent failures from one client,
  IPv6 /64 grouping, the 16 Ki client cap, that `X-Forwarded-For` is ignored from untrusted peers,
  and that the rightmost untrusted address is used behind trusted proxies.
- Run the checks required by [WORKFLOW.md](WORKFLOW.md) and identify any checks
  that could not be run.
