# Agent guidance

## Project direction

Build a Go web server that accepts turn-file uploads for play-by-email (PBeM)
games. It replaces the custom e-mail client code in the existing games: players
upload files over HTTPS instead of mailing them.

- Uploads come from unauthenticated clients. The only authorization is an email
  address and an upload token, checked against the `game_uploaders` table.
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
- No server, database, or command exists yet. Do not document the planned
  commands, routes, or packages below as implemented until they are.

## Planned commands

Use `github.com/peterbourgon/ff/v4` for flags and `internal/dotenv` for
environment files. Explicit flags override environment values, which override
defaults.

| Command | Purpose |
| --- | --- |
| `cmd/fuingod` | The web server. Opens an existing database; never creates or migrates one. |
| `cmd/fuingodb` | Database administration: `create` and `migrate`. |
| `cmd/fuingo` | Future phase: the operators' CLI for managing games, uploaders, API accounts, and game managers. It is the only way to create accounts outside development and agent. Do not implement it until the user starts that phase. |

## Data model

Game codes and API account emails can change, so rows are identified by integer
IDs, never by those values. Declare these IDs `INTEGER PRIMARY KEY AUTOINCREMENT`:
without `AUTOINCREMENT`, SQLite may reuse the ID of a deleted row, and IDs must
never be reused.

| Table | Columns | Keys |
| --- | --- | --- |
| `games` | `id`, `code`, `is_active` | PK `id`; `code` unique |
| `game_uploaders` | `game_id`, `email`, `credential_hash`, `is_active` | PK `(game_id, email)`; `credential_hash` unique; FK `game_id` → `games` |
| `uploads` | `id`, `game_id`, `email`, `content_hash`, `uploaded_at` | PK `id`; FK `(game_id, email)` → `game_uploaders` |
| `accounts` | `id`, `email`, `password_hash` | PK `id`; `email` unique (future `/api/` phase) |
| `game_managers` | `game_id`, `account_id` | PK `(game_id, account_id)`; FKs to `games` and `accounts` (future `/api/` phase) |

- `game_uploaders` holds the upload credentials. `email` is stored lowercased.
  `credential_hash` is the hash described below; the plaintext token is never
  stored. Because it is unique, a match identifies exactly one uploader and game.
- `uploads` records each successful upload by game and uploader, so its history
  survives a change of the uploader's token. `uploaded_at` is UTC.
- `accounts` holds API accounts only, with lowercased emails. Upload credentials
  are never stored there. `game_managers` lists the games each API account
  manages.
- Every foreign key is `ON DELETE RESTRICT`: deleting a row that others refer to
  fails instead of removing history. Revoke access by clearing `is_active`.

Until `cmd/fuingo` exists, the only rows in `games` and `game_uploaders` come
from the default data and the development/agent helper in
[TESTING.md](TESTING.md).

## Upload requirements

The web form has two fields, email address and upload token. The file picker
stays disabled until both are filled in. The server handles each upload in this
order:

1. **Size limit.** Reject any upload larger than 64 KiB (65,536 bytes). Enforce
   the limit while reading the request (for example, with `http.MaxBytesReader`),
   not after buffering an unbounded body.
2. **Sanity check.** Reject empty files. Skip one leading UTF-8 BOM (`EF BB BF`)
   if present. The next 128 bytes, or all remaining bytes if fewer, must each be
   printable ASCII (`0x20`–`0x7E`), tab (`0x09`), LF (`0x0A`), or CR (`0x0D`). Any
   other control byte, or any byte above `0x7F`, fails the check. Bytes beyond
   that sample are not checked. A file containing only a BOM is empty.
3. **Authorization.** Lowercase the email with `strings.ToLower`; apply no other
   normalization unless the user agrees to it. Compute the credential hash as
   `HMAC-SHA256(pepper, lowercased-email + "\x00" + token)` and look it up in
   `game_uploaders`. Both the matching uploader and its game must be active; an
   inactive one fails like a missing row. There are no salts. Compare hashes in
   constant time.
4. **Storage.** Name the file with the lowercase hex SHA-256 of its contents.
   If that file already exists, keep it and do not rewrite it. Otherwise write a
   temporary file in the upload directory and rename it into place, so a partial
   write is never visible under the final name.
5. **Record.** Insert an `uploads` row with the matched uploader's game and
   email, the content hash, and the UTC timestamp. Every successful upload gets its own row, including a
   repeat of content already stored.
6. **Response.** Return success or failure to the client. Use one generic
   message for every authorization failure, so a response never reveals whether
   an email exists or which credential was wrong. Size and sanity failures may say
   what was wrong.

### Secrets

- The pepper comes from `FUINGO_PEPPER`, never from the database, source
  control, or a shared `.env` file. Keep it in the process environment or an
  ignored `.local` file. `fuingod` refuses to start, and `fuingodb` refuses to
  create or seed a database, if it is missing or empty.
- Changing the pepper invalidates every stored credential hash. Do not rotate it
  without a plan agreed with the user.
- Never log tokens, the pepper, or file contents.
- The default tokens and passwords in [TESTING.md](TESTING.md) are fixed,
  public test values. They are the only credentials allowed in source control.

## User interface

- Build the form with HTMX and Tailwind CSS. Use server-rendered HTML fragments
  for responses; do not introduce a JavaScript framework.
- Start with a native `<input type="file">` styled with Tailwind's `file:`
  modifier. The user may later replace it with a Tailwind UI kit component.
- Load Tailwind CSS (the `@tailwindcss/browser` build) and HTMX from a CDN,
  pinned to exact versions. Do not add a Tailwind CLI, watcher, or generated CSS
  to the repository; the user will set those up if the project adopts a Tailwind
  UI kit component.

## Future phase: the `/api/` routes

Do not implement these until the user starts the phase.

- Smarter clients will submit files through `/api/` routes without the web form.
- `/api/` requests authenticate with the `Authorization` header.
- `/api/` routes accept only API accounts, which never use the web pages. Upload
  accounts never authenticate to `/api/`. Keep the two kinds separate; see
  [TESTING.md](TESTING.md).
- API account passwords are hashed with bcrypt at `bcrypt.MinCost`. This is an
  accepted risk; do not raise the cost or propose doing so.
- The API adds listing and management of uploaded files. An API account may act
  only on the games it manages in `game_managers`.

## Verification

- Test observable HTTP and command behavior, not private implementation details.
- Cover the upload contract: the 64 KiB boundary (65,536 bytes accepted, 65,537
  rejected), empty and BOM-only files, a BOM followed by valid text, every
  boundary byte of the sanity check, files shorter than the 128-byte sample, a
  disallowed byte as the last sampled byte (rejected) and the first byte after
  the sample (accepted), with and without a BOM, email case folding,
  wrong email, wrong token, an inactive uploader, an inactive game, a missing
  pepper, duplicate content, and identical
  failure responses for unknown emails and wrong tokens.
- Run the checks required by [WORKFLOW.md](WORKFLOW.md) and identify any checks
  that could not be run.
