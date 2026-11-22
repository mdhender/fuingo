# Agent guidance

## Project direction

Fuingo replaces e-mail for the user's play-by-email (PBeM) games.
Players upload their orders and download their reports over HTTPS; game masters (GMs) upload reports and download orders, on the web or with tools that use the `/api/` routes.
Nothing in a game depends on mail reaching a player.

- Fuingo is a file exchange shaped like a PBeM game, not a general file share.
  Files live at fixed levels of one hierarchy, described below; do not add arbitrary folders.
- It serves the user's own games from one instance.
  There are no tenants, quotas, or GM sign-up.
  Another GM who wants Fuingo runs their own.
- Fuingo manages access.
  A file's game, turn, and player come from where it was put and who put it there, never from the file's contents.
- Fuingo tells nobody that a file has arrived.
  GMs announce turns where the players already talk, such as Discord.
  The only mail Fuingo sends is for accounts: invitations and password resets.
- Fuingo does not try to stop a player uploading the wrong file.
  A clear page is the only safeguard; do not add checks of a file's contents against its turn or game.
- Distinguish agreed requirements from proposals.
  Resolve consequential design questions with the user rather than inventing behavior.
  The [open questions](#open-questions) are listed at the end.

## Status: the design is partly built

The user agreed this design on 2026-11-22, and it is being built in the steps the user asks for.
[README.md](README.md), [API.md](API.md), and [TESTING.md](TESTING.md) describe the code as it is, so they describe only what is built.

- Built: systems, games in systems, a game's GMs and players, the administrator's commands for them, turns, file storage, and the `/api/` routes for a game's players, turns, and files.
  A GM creates a turn by naming its number, which must be exactly the game's next (0 first, then no gaps); anything else gets `409`.
- Removed on 2026-11-22: the previous design's header line, upload tokens, pepper (`FUINGO_PEPPER`), unauthenticated `POST /upload` form, `reset-token` route, and `POST /api/games`.
  Do not bring any of them back.
- Files through `/api/`, for GMs: upload to a game, a turn, or a player's folder; list a folder; list a game's uploads after a given ID; download; and withdraw the GM's own file.
  A file is uploaded as the request's body with its name in the `name` query parameter, and afterwards addressed by its upload ID within its game.
  Listings leave out withdrawn files unless asked with `withdrawn=include`; a GM can still read a withdrawn file's record and contents.
  The API's size limit is `--max-api-upload` (`FUINGO_MAX_API_UPLOAD`).
  Downloads return the stored bytes only: normalization is not built.
- Mail, built on 2026-11-22: invitations and password resets, sent through Mailgun.
  `fuingo account create`, `fuingo game add --player`, and a GM adding a player through `/api/` mail an invitation to an account they create; `fuingo account reset-password` and the public page `/account/reset` mail an active account a new link.
  A link leads to `/account/password`, works once, and expires after 72 hours; sending an account a new link cancels its earlier ones.
  An account without a password is always sent an invitation, so `/account/reset` also replaces an expired one.
  Setting a password signs nobody in and ends all of the account's sessions, on the web and in `/api/`.
  A password is at least 8 characters and at most 72 bytes.
  `/account/reset` answers the same for every email and is limited per client address, 5 requests and then one per minute.
  `FUINGO_MAILGUN_SANDBOX_TO_ADDR` redirects every message to one address, for a Mailgun sandbox, and is refused in production.
  `fuingod` does not start in production without the Mailgun settings, and needs `--public-url` (`FUINGO_PUBLIC_URL`) outside development and agent.
  No command sets or reads a password: `fuingo account set-password` and `account create`'s password were removed.
- Web sign-in and sessions, built on 2026-11-22: `/sign-in`, `POST /sign-out`, and the page at `/`, which shows only who is signed in and sends anyone else to `/sign-in`.
  The session cookie is `__Host-fuingo_session`: `Secure`, `HttpOnly`, `SameSite=Lax`.
  Every use of a session renews it for 30 days.
  Sessions are rows of `web_sessions`, deleted when signed out, expired, or ended by a password reset.
  An inactive account's session is refused but kept, so it works again if the account is enabled before it expires.
  The failure limit is one limiter shared by `/sign-in` and `/api/login`.
- The pages, built on 2026-11-22: `/` (the account's games, or its only game), `/systems/{system}`, the game's page, a turn's page, a player's folder, the download of a file, and a GM's page of players.
  They are in `internal/server`: `files.go` (folders, uploads, downloads, withdrawals) and `browse.go` (the pages, and a GM's changes to turns and players).
  A page's paths mirror the `/api/` ones without the `/api` prefix; a file downloads at `.../uploads/{id}`.
  A folder is one template, `folder`, which is also the fragment HTMX swaps in after an upload or a withdrawal; a request without `HX-Request` is redirected to the folder's page instead.
  A player's own folder is part of the turn's page.
  An upload is a `multipart/form-data` post of one part named `file`, whose file name is the upload's name.
  The limits are `--max-player-upload` (`FUINGO_MAX_PLAYER_UPLOAD`) and `--max-gm-upload` (`FUINGO_MAX_GM_UPLOAD`).
  A closed turn refuses a player's upload and withdrawal with `409`, checked inside the transaction that would store them.
  A player is shown "you" or "the GM" as a file's uploader, never the GM's email.
  A signed-out HTMX request gets `401` with `HX-Redirect`; anything unreachable gets `404` with `HX-Refresh`.
- The way back after signing in, built on 2026-11-22: a signed-out `GET` is sent to `/sign-in?next=PATH`, the form carries `next`, and signing in redirects there.
  An HTMX request's way back is the path of its `HX-Current-URL`; any other `POST` has none.
  `localPath` in `session.go` decides what is followed: a path on this origin, at most 512 bytes, with no `//` start, backslash, white space, control character, or fragment, and not `/sign-in`, `/sign-out`, or under `/api/`.
  Anything else leads to `/`; never redirect to a `next` that has not been through it.
- Not built: system-level files and normalized downloads.
- Migration 3 moved the games of a database made under the previous design into a system named `legacy` and dropped its stored uploads.
- No deployment holds live data.
  The server called production is a test server, so no step needs a cutover.
- When a step builds or replaces something, update this section and the three documents above in the same commit.

## Terms

Use these words consistently, in code, documents, and messages.

| Term | Meaning |
| --- | --- |
| System | A game such as Olympia or Atlantis: the top level of the hierarchy. |
| Game | One instance of a system, such as Olympia / G3. Its code is short and unique within its system, not across systems. |
| Turn | A numbered step of a game. Turn 0 is the setup; numbers only increase. |
| Account | Someone who can sign in, identified by an email address. |
| Player | An account that a GM has given access to one game. The same account is a different player in each game it plays. |
| GM | An account that manages one game. An account may be a GM of some games and a player in others. |
| Administrator | The user, working on the server's command line. Not a role an account can hold. |

"Game manager" is the previous design's word for GM.
It remains in the schema (`game_managers`, `active_managers`) and in `fuingo game add --manager`.

## The hierarchy

Every file is at one of four levels:

```text
Olympia                 system
Olympia / G3            game
Olympia / G3 / 1        turn
Olympia / G3 / 1 / 5    player 5's folder in turn 1
```

- A player's number is an ID that Fuingo assigns.
  It is never reused and may be unique across all games.
  It means nothing to the game; it matters only between Fuingo and the tool a game uses to pull and push files.
  A player need never see their own number.
- Every active player has a folder in every turn of their game.
  Folders are not created or stored; they follow from the players and the turns.
- A turn's number is an integer.
  What a game calls the turn in its reports, such as "Spring of Year 5", is the game's business.

### Who may do what

| Level | Adds files | Reads files |
| --- | --- | --- |
| System | The administrator | Every account that is an active player or GM in any of the system's games |
| Game | The game's GMs | The game's GMs and players |
| Turn | The game's GMs | The game's GMs and players |
| Player folder | That player, while the turn is open; the game's GMs, at any time | That player and the game's GMs |

- A player sees and may download every file in their own folder, whoever put it there: their orders, their reports, and anything else the GM adds.
- A player never sees another player's folder, or that it exists.
- GMs see everything in the games they manage and nothing in any other game.
  A GM of an Atlantis game learns nothing about Olympia's games.
- Anything an account may not reach gets the same response as something that does not exist.

### Turns

- A GM creates each turn and closes it, and may reopen a closed turn.
  Uploads are refused for a turn that does not exist.
- Closing a turn stops players uploading to it and withdrawing from it.
  It changes nothing else: players still read their folders, and GMs still add files.
- Any number of turns may be open at once, so a player who will be away can upload orders for later turns.

### Files

- Files are text.
  Orders are the files a player uploads; reports are the files a GM uploads.
  Fuingo records who uploaded a file, and does not otherwise distinguish kinds of file.
- A file has a name, which Fuingo stores and never interprets.
  A name is at most 48 bytes.
  Each game has its own naming convention, and that is the game's business.
- Some games answer a player's orders with the results of parsing them, so the player can fix mistakes before the turn closes.
  That is an ordinary file the game's tool uploads to the player's folder.
- Every upload is kept.
  A second file does not replace the first, even with the same name; the folder lists them in the order they arrived.
  The game's tool decides which orders count, normally the last before the turn closed.
- A file can be withdrawn.
  A withdrawn file is no longer listed for the player or given to tools, but its record stays, so a GM can still see what was uploaded and when.
  A player may withdraw a file they uploaded while its turn is open.
  A GM may withdraw a file they uploaded at any time, to recover from putting a report in the wrong folder.
- Nothing is ever deleted.
  Uploads are permanent history; do not add a command or route that deletes them.
- Empty files are refused.
  A player with no orders for a turn withdraws their file rather than uploading a blank one.
- Every upload may start with one optional UTF-8 BOM (`EF BB BF`) and must be valid UTF-8, whoever uploads it.
  The only control characters it may hold are tab (`0x09`), LF (`0x0A`), and CR (`0x0D`); any other, such as NUL, is refused.
- There are three size limits, each configurable on its own: a player's upload on the web, 64 KiB (65,536 bytes) by default; a GM's upload on the web, 1 MiB (1,048,576 bytes) by default; and an upload through `/api/`, 1 MiB by default.
- Check the whole file, never a sample of it.
  A file containing only a BOM is empty.
- Enforce a size limit while reading the request (for example, with `http.MaxBytesReader`), not after buffering an unbounded body.
  A limit applies to new uploads only: changing it never affects a file already stored.
- Store every file exactly as uploaded, byte for byte, including a BOM and its line endings.
  Fuingo does not normalize what it stores; that is each game's responsibility.
  A tool may ask the API to normalize a file as it is downloaded, as described under the `/api/` routes.
- A report may be HTML.
  Serve every file so that it cannot run scripts on Fuingo's origin, as a download or sandboxed, because that origin holds sessions.
- When a GM or a tool lists or downloads files, Fuingo gives the system, game, turn, and player of each, with its name, who uploaded it, and when.

## Accounts and access

- Accounts are by invitation only.
  Nobody can sign up.
- The administrator makes the first accounts from the command line.
  Creating an account sends its invitation; no command sets or reads a password.
- An invitation and a password reset are each a link mailed to the account's address, which expires after 72 hours.
- Fuingo does not handle mail that fails.
  The administrator sees bounces through Mailgun.
- An account's email cannot be changed, for now.
  When the user asks for it, look into the best practices before proposing a design.
- A GM gives an account access to a game, which makes it a player there.
  If the email has no account, that creates one and sends the invitation.
  This is all a GM may do to an account: a GM never sets, sees, or resets a password, and never changes an account's email.
- An account's owner sets and resets their own password, through mail sent to the account's address.
  The administrator may start a reset for an account, and never chooses its password.
- An account has one password, used for the web and for `/api/`.
  There are no per-game credentials and no upload tokens.
- A browser stays signed in with a session: a random token in a cookie, stored in the database only as its hash, so the server can end it.
  A web session lasts 30 days and is renewed each time it is used.
  Signing out ends that session.
  Resetting a password ends every other session of the account, on the web and in `/api/`.
  Follow [PROXIES.md](PROXIES.md) for the cookie.
- Fuingo sends its mail through Mailgun.
  The Mailgun credentials are secrets: they come from the environment and have no flags.
- The administrator creates each system, creates each game in it, and assigns the game's GMs.
  A GM is assigned to a game, never to a system: a system has no GMs of its own.
  GMs do not create games or systems.
- Every upload, download, and listing needs a signed-in account.
  Nothing is open to an anonymous client except signing in and the account mail flows.
- Access is revoked by clearing `is_active` on an account, a game, a player, or a GM, never by deleting a row.
  An inactive account can do nothing, whatever its player and GM rows say.
- Failed sign-ins are limited per client address, never per email, so guessing cannot lock an account's owner out.
  Each client may fail 5 times, then once more per minute.
  A client over its limit gets `429 Too Many Requests` with `Retry-After` instead of a check, even with a valid password.
  Reserve a failure before the check and return it on success or server error, so concurrent guesses cannot exceed the limit.
  Every failed sign-in gets one generic response, so none reveals whether an email has an account.
- The client address is the connection's peer, unless the peer is a trusted proxy: then it is the rightmost `X-Forwarded-For` address that is not a trusted proxy.
  Trust only the proxies configured in `FUINGO_TRUSTED_PROXIES`.
  Group IPv6 clients by their /64 prefix.
  The limits are kept in memory and reset when the server restarts.
  Track at most 16,384 (16 Ki) clients; a new client beyond that replaces the one that has regained the most attempts.
- Passwords are hashed with bcrypt at `bcrypt.MinCost` and are at most 72 bytes.
  The cost is an accepted risk; do not raise it or propose doing so.

### Secrets

- Never log passwords, session tokens, invitation or reset links, mail credentials, or file contents.
- Store session tokens and the tokens in invitation and reset links only as hashes, never as issued.
- The default passwords in [TESTING.md](TESTING.md) are fixed, public test values.
  They are the only credentials allowed in source control.

## Sources and repository layout

- Read and follow [WORKFLOW.md](WORKFLOW.md) before working.
  It owns the project's bug-priority, build/test, versioning, Git, and CI policies; do not duplicate or override those policies here.
- Read [PROXIES.md](PROXIES.md) before implementing HTTP startup, listeners, redirects, cookies, or CSRF protection.
  The development origin `https://htmx-app.localhost:8443` is shared by all PBeM projects through the user's Caddy configuration; keep it.
- Read [SQLITE3.md](SQLITE3.md) before implementing database code.
- Read [TESTING.md](TESTING.md) before changing default accounts, in-memory databases, or the development/agent HTTP helpers.
- `go.mod` defines the module `github.com/mdhender/fuingo`.
- `version.go` provides the root package's version API.
- `internal/dotenv` loads environment files; `internal/cerrs` defines constant errors.
  Use both rather than adding alternatives.
- `internal/store` wraps the SQLite database: `Create`, `Migrate`, and `Open` for files, plus `OpenPrivateMemory`, `CreateSharedMemory`, and `OpenSharedMemory`.
- `internal/sqlitedb` implements the SQLite file contract in [SQLITE3.md](SQLITE3.md) for any schema.
  `internal/store` wraps it with Fuingo's application ID and migrations; do not copy the file handling into another package.
- `internal/devdata` holds the default system, games, players, and accounts; `internal/cli` holds the flag and environment-file parsing shared by the commands.
- `internal/clientip` finds the client's address behind trusted proxies; `internal/ratelimit` limits failures per client.
  Both carry over to sign-in unchanged.
- `internal/server` assembles the routes (the sign-in and sign-out routes in `session.go`, the signed-in pages in `files.go` and `browse.go`, the account pages `/account/password` and `/account/reset` in `account.go`, the vendored HTMX under `/static/`, the `/api/` routes, and the development/agent helpers) behind `http.NewCrossOriginProtection`.
  HTMX 2.0.11 is vendored as `internal/server/static/htmx-2.0.11.min.js`.
- `internal/api` is the `/api/` handler.
- `internal/mail` sends mail through Mailgun, and reads its settings from the environment; `internal/mail/mailtest` records mail for tests.
  `internal/links` makes, mails, and redeems invitation and reset links, and checks passwords.
  Everything that sends an account a link goes through `links.Service`.
- `internal/names` checks and lowercases codes and emails, and checks file names (`names.File`) and file contents (`names.Text`).
  Every command and route that accepts one goes through it.
- `contrib/systemd/fuingod.service` runs `fuingod` as a systemd service; README.md documents its setup.
  Keep the two in sync, and keep the unit's `TimeoutStopSec` longer than `fuingod`'s shutdown timeout.
- `cmd/fuingod`, `cmd/fuingodb`, and `cmd/fuingo` exist; [README.md](README.md) documents their flags and environment variables, and [API.md](API.md) the `/api/` routes.
  Keep both current when changing any of them.

## Commands

Use `github.com/peterbourgon/ff/v4` for flags and `internal/dotenv` for environment files, through `internal/cli`.
Explicit flags override environment values, which override defaults.
Every flag `--name` has an environment variable `FUINGO_NAME`; secrets have no flag.

The exception is a flag that changes what a command does, or names what it acts on, rather than configuring it, such as `fuingo version --schema` or a flag naming the account or game to change.
Such a flag is honored only on the command line, never from the environment: set in an environment file, it would silently change the command for every later run.
Pass its name to `cli.Parse`, which removes its environment variable before applying the environment, and document it in the command's help and package comment.

Each program owns one area.
Put a new command in the program that owns what it manages, even when another program could reach the same thing:

| Command | Purpose |
| --- | --- |
| `cmd/fuingo` | Application data. The administrator's CLI. |
| `cmd/fuingodb` | The database: `create` and `migrate`. |
| `cmd/fuingod` | The server. Opens an existing database; never creates or migrates one. |

- **`cmd/fuingo` is the administrator's tool**, and the only way to do what only the administrator may: create a system, create a game in it, assign a game's GMs, add a file at the system level, and start an account's password reset.
  Every command that creates or changes application data belongs here.
- **`cmd/fuingodb` manages the database itself**: creating, migrating, and any future maintenance of the database file and its schema.
  Never add one of these, such as `migrate`, to `fuingo`.
- **`cmd/fuingod` manages the server**: it serves the web pages and the `/api/` routes, and owns the server's startup flags.
  It opens an existing database; it never creates or migrates one, and it has no subcommands for data.
- **A game's own tool is a separate program**, outside this repository's commands.
  It reaches Fuingo only through `/api/`, as an account that is a GM of the game, and never opens Fuingo's database.
  Fuingo knows nothing of a game's own ideas, such as Olympia's factions: the tool maps them to Fuingo's player numbers and names each file.
  `cmd/olympia`, the Olympia G3 tool built for the previous design, was removed on 2026-11-22; build a replacement only if the user asks.

`fuingo` commands follow the noun-verb pattern, `fuingo <noun> <verb>`: each noun has only the verbs that make sense for it, and `fuingo <noun> --help` lists them.
`--db` is a root flag, as in `fuingo --db PATH game create --system SYSTEM CODE`, because almost every command uses the database.
Commands that use it open it with `store.Open` and fail unless it is at the schema version the program expects.

[README.md](README.md) lists the commands that exist.
Add, change, and remove commands only as the user asks.
Codes and emails are lowercased with `strings.ToLower`; apply no other normalization unless the user agrees to it.

## Data model

The schema holds `systems`, `games`, `accounts`, `game_players`, `game_managers`, `turns`, `file_contents`, `uploads`, `api_sessions`, `account_links`, and `web_sessions`.
`account_links` holds each invitation and reset link by the hash of its token, with when it expires and whether it was used or canceled; its rows are kept.
`uploads` allows a row with no game, for a system-level file, but nothing writes one yet, and its uploader must be an account.
These rules hold whatever the tables are:

- Codes and emails can change, so rows are identified by integer IDs, never by those values.
  Declare these IDs `INTEGER PRIMARY KEY AUTOINCREMENT`: without `AUTOINCREMENT`, SQLite may reuse the ID of a deleted row, and IDs must never be reused.
  A player's number is such an ID.
- Every foreign key is `ON DELETE RESTRICT`: deleting a row that others refer to fails instead of removing history.
- A game's code is unique within its system.
  Codes and emails are stored lowercased.
  The code of a system or a game is at most 32 UTF-8 characters.
- Codes and file names appear in URLs, on the page, and in the shell scripts many games use, whose authors do not always quote or escape them, so they hold only safe characters.
  A code holds letters and digits in any script, `-`, and `_`.
  A file name holds only ASCII: the letters `A`–`Z` and `a`–`z`, the digits, `-`, `_`, and `.` inside it, never at its start or end.
  Codes may hold letters from any script because only the administrator creates them; anyone who uploads chooses a file name.
  Everything else is refused, including spaces and all other white space, control characters, `/`, `\`, the HTML special characters `<`, `>`, `&`, and `;`, and every kind of quote.
- A system has no settings of its own.
  Every system shares the server's settings, such as the size limits.
- `accounts` has one row per email, for players and GMs alike.
  Whether an account plays or manages a game is recorded only in the rows that join it to that game.
- A turn is a row that a GM created, with its number and when it was closed (`closed_at`, NULL while it is open).
- Each upload is a row recording where it was put (system, game, turn, or player folder), who uploaded it, its name, when it arrived (UTC), and whether and when it was withdrawn.
  Record the uploader's email as it was then, so the history keeps it if the account's email later changes.
- Files are stored only in the database, never on the filesystem.
  Contents are kept once, keyed by the lowercase hex SHA-256 of the contents, and shared by every upload of the same bytes, across players and games.
  Never rewrite contents that are already stored, and never let a route reach contents by their hash alone: access is always checked against an upload.
- Store an upload's contents and its row in one transaction, so contents are never stored without a row.
- Keep the rules for being active in views, as `active_players` and `active_managers` do now, so queries do not repeat them.

## User interface

- Build the pages with HTMX and Tailwind CSS.
  Use server-rendered HTML fragments for responses; do not introduce a JavaScript framework.
- A signed-in account navigates the hierarchy as a file tree or with breadcrumbs, and sees only the branches it may reach.
  A player in one game lands in that game.
- Players and GMs use the same pages; a GM's show more and allow more.
- A player uploads by dropping a file on their folder in an open turn, or choosing one.
  The page must make the game and turn of the folder obvious, before the drop and in the receipt after it, and offers no drop target in a closed turn.
- The drop zone surrounds a visually hidden `<input type="file">` and uploads as soon as a file is selected or dropped, with no Upload button.
  Its script may refuse a file before sending it, showing the message in the page, never in a browser dialog.
  Such checks are conveniences; the server enforces the rules.
- Vendor HTMX: commit the minified file for one exact release, with its version in the file name, and serve it from the binary with `embed`.
  Do not load HTMX from a CDN.
- Load Tailwind CSS (the `@tailwindcss/browser` build) from a CDN, pinned to an exact version.
  Do not add a Tailwind CLI, watcher, or generated CSS to the repository; the user will set those up if the project adopts a Tailwind UI kit component.

## The `/api/` routes

The `/api/` routes serve the GMs' tools.
A tool can do through them what its GM can do on the web: list and download a game's files, upload files to its folders, withdraw the GM's own files, manage its players, and create and close its turns.
[API.md](API.md) is the reference for clients: every route's request, response, status codes, and an example.
Keep it current with every change to a route, and keep the route table in [README.md](README.md#api) in step with it.
Add, change, and remove routes only as the user asks.

These rules carry over from the routes that exist:

- `POST /api/login` exchanges an account's email and password for a bearer token valid for 24 hours, stored only as its SHA-256.
  Every other route requires `Authorization: Bearer TOKEN`.
  A token works only while its account is active.
- An account may act only on the games it is an active GM of, while the account is active; the game may be inactive.
  Any other game, and anything outside a managed game, gets the same `404` as one that does not exist.
- A player is addressed by its number, never by email.
- The `/api/` routes go through the same `http.NewCrossOriginProtection` as the pages.
  Command-line clients send neither `Origin` nor `Sec-Fetch-Site`, so they pass; browsers on other origins are refused.
- Every error from a path under `/api/` is a JSON `api.Error`, including an unknown path, a wrong method (with its `Allow` header), and a cross-origin request the server refuses, which `internal/server` answers through `api.Forbidden`.
  Never let a plain-text error out of `/api/`.
- A request body that is JSON must be exactly one JSON object with only the documented fields, read with `decodeOnly`.
  Unknown fields and anything but white space after the object get `400`.
- A download gives the contents' hash in `X-Content-SHA256`.
- A download returns the file exactly as stored unless the client asks for it normalized, such as without its BOM or with its line endings made uniform.
  Normalizing changes only that response, never what is stored.

A tool can ask for the uploads that arrived after the last one it saw, so a game can answer orders with parsing results soon after they arrive.

## Open questions

Settle each of these with the user before building what depends on it.

- Which normalizations a download offers, how a client asks for them, and which contents `X-Content-SHA256` describes when it does.
- Renaming a system or game.
- Naming a turn, as a later feature.
- Pagination of listings.
- Whether to build a new Olympia G3 tool against this design (issue #1).

## Verification

- Test observable HTTP and command behavior, not private implementation details.
- Cover access: that a player reaches only their own folders, that a GM reaches only the games they manage, that an inactive account, player, GM, or game is refused, and that what an account may not reach is indistinguishable from what does not exist.
- Cover turns: uploads to a turn that does not exist, to an open turn, to a closed one, and to a reopened one; that closing blocks only players; and several open turns.
- Cover uploads: each of the three size limits at its boundary, at its default and at a configured value, and that each is independent of the others; empty and BOM-only files; a BOM followed by a valid file; bytes above `0x7F` in valid UTF-8 (accepted); invalid UTF-8 at the start, in the middle, and at the end of a file (rejected); and NUL and other control characters (rejected) beside tab, LF, and CR (accepted), for a player and for a GM.
- Cover names: a 32-character code and a 33-character one, counted in characters and not bytes; a 48-byte file name and a 49-byte one; non-ASCII letters in a code (accepted) and in a file name (rejected); and each kind of refused character, including `<`, `>`, `&`, `;`, quotes, `/`, `\`, a space anywhere in a file name, and a leading or trailing `.` in one.
- Cover storage: that a download returns exactly the bytes uploaded, including a BOM and CRLF line endings, that a normalized download leaves the stored file unchanged, and that lowering a size limit leaves stored files readable.
- Cover repeated and withdrawn files: that a second upload replaces nothing, that a withdrawn file leaves the listings and its record remains, that a player cannot withdraw after the turn closes or withdraw a GM's file, and that a GM can withdraw their own file in a closed turn.
- For duplicate content, cover players in the same game and in different games with identical files, concurrent uploads, and that stored contents are not rewritten.
- Cover failure limiting: the limit and `Retry-After`, recovery over time, that successes do not count, that a valid password is refused while limited, that other clients (including the same email from another address) are unaffected, concurrent failures from one client, IPv6 /64 grouping, the 16 Ki client cap, that `X-Forwarded-For` is ignored from untrusted peers, and that the rightmost untrusted address is used behind trusted proxies.
- Cover accounts: that creating an account, by the administrator or by a GM adding an unknown email, sends one invitation, that a GM cannot set or reset a password, that a link is refused after 72 hours, and identical sign-in failures for emails with and without an account.
- Cover sessions: that one is renewed by use and refused after 30 days unused, that signing out ends it, that a password reset ends the account's other sessions, and that an inactive account's session is refused.
- Cover that refused requests change nothing, that no password, token, or link is logged, and that unknown paths, wrong methods, and refused cross-origin requests under `/api/` get JSON errors.
- Run the checks required by [WORKFLOW.md](WORKFLOW.md) and identify any checks that could not be run.
