# The fuingo API

Reference for anyone, human or agent, writing a tool that calls `fuingod`'s
`/api/` routes. It describes what a client sends and receives. For the rules
that govern changing the server, see [AGENTS.md](AGENTS.md).

The API is for **game masters (GMs)** and their tools. Today it lists the
games an account manages, manages each game's players, creates, closes, and
reopens its turns, and uploads, lists, downloads, and withdraws its files.

## Quick start

```sh
FUINGO=https://htmx-app.localhost:8443   # the development origin

# 1. Log in. The token lasts 24 hours.
TOKEN=$(curl -s "$FUINGO/api/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"acct01@example.com","password":"worser-choices-2001"}' \
  | jq -r .token)

# 2. Call a route with the token.
curl -s "$FUINGO/api/systems/system01/games/game01/players" \
  -H "Authorization: Bearer $TOKEN"
```

The email and password above are one of the public default accounts that
exist only in `development` and `agent`; see
[Local development](#local-development).

## Systems and games

A **system** is a game such as Olympia. A **game** is one instance of it,
such as Olympia / G3. A game's code is unique only within its system, so
every game route names both:

```text
/api/systems/{system}/games/{game}/...
```

Only the administrator creates systems and games and assigns a game's GMs,
with the `fuingo` command on the server; see "Creating systems, games, and
accounts on the server" in [README.md](README.md). The API creates neither.

## Conventions

- **Base URL.** Every route is under `/api/` on the server's public HTTPS
  origin. In development that is `https://htmx-app.localhost:8443`.
- **Requests.** Except when uploading a file, which sends the file itself
  (see [Files](#files)), a request with a body sends a JSON object with
  `Content-Type: application/json` (a `charset` parameter is accepted),
  at most 4 KiB, containing only the documented fields. The object must be
  the whole body: white space around it is allowed, anything else after it
  is not. An unknown field, a field of the wrong type, trailing data, or a
  larger body gets `400`.
- **Methods.** Each route accepts only the method shown. A `GET` route
  also answers `HEAD`.
- **Paths** must be written exactly as shown, with no trailing slash and
  no doubled slashes. A trailing slash gets `404`. A path that needs
  cleaning, such as `/api//games`, gets a redirect to the clean path
  rather than an error; do not rely on following it.
- **Responses.** JSON with `Content-Type: application/json`, except a
  download, which is the file. Every error is JSON. Every response has `Cache-Control: no-store`.
- **Codes** of systems and games are case-insensitive: the server
  lowercases them, and returns them lowercased. A code is 1 to 32
  characters: letters and digits in any script, `-`, and `_`. A path with
  anything else where a code belongs gets `404`.
- **Emails** are case-insensitive in the same way. An email is one or more
  printable ASCII characters (`0x21`–`0x7E`), with no spaces.
- **IDs** are positive integers, never reused. A player is addressed by its
  ID, the player's number, never by email.
- **Timestamps** are UTC, always in the form
  `2026-11-21T22:06:29.628072000Z` (nine fractional digits), so they sort as
  text.
- **Browsers.** The routes sit behind cross-origin protection. A
  command-line client, which sends neither `Origin` nor `Sec-Fetch-Site`,
  passes. A request from a page on another origin gets `403`. There is no
  CORS support.
- **No pagination.** List routes return everything.
- **Nothing is deleted.** No route deletes a game, a player, a turn, or a
  file. Revoke access by disabling; take a file back by withdrawing it.

## Errors

Every error from a path under `/api/` has a JSON body with one field,
including an unknown path, a wrong method, and a refused cross-origin
request:

```json
{"error":"not found"}
```

Treat the message as text for people; branch on the status code.

| Status | Meaning |
| --- | --- |
| `400` | The body is not exactly one valid JSON object, has an unknown or badly formed field, or a value breaks a rule above. For an upload: a bad file name or a file that is not acceptable text. Also a bad query parameter. |
| `401` | Login failed, or the bearer token is missing, unknown, or expired. On routes other than login, the response has `WWW-Authenticate: Bearer realm="fuingo"`. Log in again. |
| `403` | Cross-origin request refused, or withdrawing a file another account uploaded. |
| `404` | The game or item does not exist, or the account does not manage the game. The two cases are deliberately identical, and identical to an unknown path under `/api/`. |
| `405` | Wrong method for the path. The `Allow` header lists the methods it accepts. |
| `409` | The account is already a player in the game, or the turn is not the game's next. |
| `413` | An uploaded file is larger than the server's limit. |
| `415` | A body was required and `Content-Type` was not `application/json` (for an upload, not `text/plain` or `application/octet-stream`). |
| `429` | Too many failed logins from this client address. `Retry-After` gives the seconds to wait. |
| `500` | Server error. Safe to retry later. |

Checks run in a fixed order, and the first failure decides the status:

1. `403` for a cross-origin request.
2. `404` for an unknown path, or `405` for a wrong method.
3. `401` for a missing or invalid token (except on login).
4. `404` for a game the account does not manage.
5. `415`, then `400` (or `413`), for the body and query parameters.
6. `404`, `403`, or `409` for the item itself.

So the first two do not depend on being logged in. A request with no
token gets `401`, never `415` or `400`, whatever its body. A bad body sent
to a game the account does not manage gets `404`, not `400`. On login,
the order is `415`, `400`, `429`, then `401`.

A proxy in
front of the server may still answer in its own format when the server is
down (for example `502`), so a robust client checks `Content-Type` before
parsing an error.

## Authentication

### `POST /api/login`

Exchanges an account's email and password for a bearer token.

```sh
curl -s "$FUINGO/api/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"acct01@example.com","password":"worser-choices-2001"}'
```

```json
{"token":"TPTV6RHA7QMMJIKCEIBPFXEHRT","expires_at":"2026-11-22T22:06:29.535977000Z"}
```

| Status | When |
| --- | --- |
| `200` | Logged in. |
| `400` | The body is not a JSON object with a non-empty `email` and `password`. |
| `401` | Any failed login. |
| `415` | `Content-Type` is not `application/json`. |
| `429` | Too many failures; see below. |

- Send the token on every other route as `Authorization: Bearer TOKEN`.
- A token is valid for 24 hours, until `expires_at`. There is no refresh
  and no logout route: log in again when it expires or a route returns
  `401`. An account may hold several tokens at once.
- A token stops working at once if its account is disabled, and works
  again if the account is re-enabled before it expires.
- **Choosing a new password ends the account's tokens.** When the owner
  sets a password through a mailed link, every token of the account stops
  working; log in again with the new password.
- Every failed login gets the same `401`, whether the email is unknown, the
  password is wrong, the account is disabled, or the account has no
  password. A client cannot tell these apart.
- **Failure limit.** Each client address may fail 5 times, then once more
  per minute, counting failed sign-ins on the web pages from the same
  address too. Beyond that every login from the address gets `429`, even
  with the right password, until `Retry-After` seconds pass. Successful
  logins and malformed requests (`400`, `415`) do not count. Do not retry
  a `401` in a loop. IPv6 clients are grouped by /64 prefix, so tools on
  one network may share a limit.
- Only an account with a password can log in. An account's owner chooses
  it through the invitation mailed to the account's address, and replaces
  it through the web page `/account/reset`. No route sets a password.

Keep the password and token out of logs, files, and command lines that
other users can read.

## What an account can reach

An account acts only on the games it **manages**: the ones the
administrator made it a GM of, with `fuingo game add --manager`.

- For any other game, every game route returns the same `404` as for a
  game that does not exist. So does a player ID or an upload ID that
  belongs to another game, even when requested through a game the account
  manages.
- The same code in another system is another game. Managing
  `olympia/g3` gives nothing in `atlantis/g3`.
- A disabled game is still fully manageable.
- If the administrator disables the account as a GM of a game, the account
  gets `404` for that game.

## Games

### `GET /api/games`

Lists the games the caller manages, in order of system code and then game
code.

```sh
curl -s "$FUINGO/api/games" -H "Authorization: Bearer $TOKEN"
```

```json
{"games":[{"system":"atlantis","code":"g3","active":false},{"system":"system01","code":"game01","active":true}]}
```

- `active` is the game's own flag. An inactive game is still listed and
  still manageable.
- A game the account has been disabled as a GM of is not listed.
- `games` is `[]`, never `null`, for an account that manages none.

## Players

A player is one account's access to one game. Its `id` is the player's
number: assigned by the server, never reused, and meaningless to the game.
The same email in two games is two players, with different IDs.

### `GET /api/systems/{system}/games/{game}/players`

Lists the game's players, active or not, in order of ID.

```sh
curl -s "$FUINGO/api/systems/system01/games/game01/players" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"system":"system01","game":"game01","players":[
  {"id":1,"email":"catbird@example.com","active":true,"account_active":true},
  {"id":2,"email":"spoonbill@example.com","active":false,"account_active":true}]}
```

- `active` is the player's own flag in this game; `account_active` is its
  account's, which only the administrator changes. A player can use the
  game only while both are true and the game is active.
- `players` is `[]`, never `null`.

### `POST /api/systems/{system}/games/{game}/players`

Makes the account with the email an active player in the game. If the
email has no account, one is created and mailed an invitation to choose
its password.

```sh
curl -s "$FUINGO/api/systems/system01/games/game01/players" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"email":"Wren@Example.com"}'
```

```json
{"system":"system01","game":"game01","id":4,"email":"wren@example.com","invitation":"sent"}
```

| Status | When |
| --- | --- |
| `201` | Added. `email` is lowercased; `id` is the player's number. |
| `400` | `email` is missing, empty, or not printable ASCII without spaces, or the body has another field. |
| `409` | That email is already a player in the game, even a disabled one. Enable it instead. |

- The body holds only `email`. A GM never sets, sees, or resets a password.
- `invitation` says what was mailed: `sent`, when the account is new and
  its invitation went out; `none`, when the account already existed, which
  is sent nothing for joining a game; or `failed`, when the account is new
  and the mail could not be sent. The player is added in every case.
- After `failed`, the player asks for a link at `/account/reset`, or the
  administrator runs `fuingo account reset-password`. That page also
  replaces an invitation that expired, after 72 hours.
- The response never holds the link.

### `POST /api/systems/{system}/games/{game}/players/{id}/disable`
### `POST /api/systems/{system}/games/{game}/players/{id}/enable`

Clears or sets the player's active flag in this game. Send no body.

```sh
curl -s -X POST "$FUINGO/api/systems/system01/games/game01/players/2/disable" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"system":"system01","game":"game01","id":2,"active":false}
```

| Status | When |
| --- | --- |
| `200` | Done. Repeating the call succeeds and changes nothing. |
| `404` | The game has no player with that ID. |

Disabling a player changes only this game: the account, and its players in
other games, are untouched.

## Turns

A turn is a numbered step of a game. Turn 0 is the setup, and the numbers
go up by one with no gaps. A turn is open when created; closing it will
stop players uploading to it, and nothing else. Any number of turns may be
open at once, so a GM can open later turns for a player who will be away.

Only the game's GMs reach these routes. A player's account gets `404`.

### `GET /api/systems/{system}/games/{game}/turns`

Lists the game's turns, in order of number.

```sh
curl -s "$FUINGO/api/systems/system01/games/game01/turns" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"system":"system01","game":"game01","turns":[
  {"turn":0,"closed":true,"created_at":"2026-11-22T12:00:00.000000000Z","closed_at":"2026-11-29T12:00:00.000000000Z"},
  {"turn":1,"closed":false,"created_at":"2026-11-29T12:00:05.000000000Z","closed_at":null}]}
```

- `closed_at` is when the turn was closed, or `null` while it is open. A
  tool can use it to decide which orders arrived in time.
- `turns` is `[]`, never `null`, for a game with no turns.

### `POST /api/systems/{system}/games/{game}/turns`

Creates the game's next turn, open. The body names the number, which must
be exactly the next one: `0` for a game with no turns, then one more than
the last.

```sh
curl -s "$FUINGO/api/systems/system01/games/game01/turns" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"turn":2}'
```

```json
{"system":"system01","game":"game01","turn":2,"closed":false,"created_at":"2026-12-06T12:00:00.000000000Z","closed_at":null}
```

| Status | When |
| --- | --- |
| `201` | Created. |
| `400` | `turn` is missing, negative, or not an integer, or the body has another field. |
| `409` | `turn` is not the game's next turn: it exists already, or it would leave a gap. The message names the next turn. |

Naming the number makes the call safe to retry: a second request for a
turn that the first one created gets `409`, never a second turn.

### `POST /api/systems/{system}/games/{game}/turns/{turn}/close`
### `POST /api/systems/{system}/games/{game}/turns/{turn}/open`

Closes a turn, or reopens a closed one. Send no body.

```sh
curl -s -X POST "$FUINGO/api/systems/system01/games/game01/turns/1/close" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"system":"system01","game":"game01","turn":1,"closed":true,"created_at":"2026-11-29T12:00:05.000000000Z","closed_at":"2026-12-06T12:00:00.000000000Z"}
```

| Status | When |
| --- | --- |
| `200` | Done. Repeating the call succeeds and changes nothing. |
| `404` | The game has no such turn. |

- Closing a turn that is already closed keeps the time it first closed.
  Reopening clears `closed_at`; closing it again records the new time.
- Write `{turn}` as its digits, such as `0` or `12`. A sign or a leading
  zero gets `404`.

## Files

A game holds files at three levels, each with its own `files` path. In
these paths `{turn}` is a turn's number and `{id}` a player's number:

| Folder | Path |
| --- | --- |
| The game | `/api/systems/{system}/games/{game}/files` |
| A turn | `/api/systems/{system}/games/{game}/turns/{turn}/files` |
| A player's folder in a turn | `/api/systems/{system}/games/{game}/turns/{turn}/players/{id}/files` |

- A folder exists when its turn does and its player belongs to the game,
  active or not. Anything else gets `404`. Create the turn first.
- A GM may upload whether the turn is open or closed.
- Every upload is kept and gets its own **upload ID**. A second file with
  the same name replaces nothing: the folder lists both, in the order they
  arrived. The tool decides which one counts.
- A file is afterwards addressed by its upload ID within its game, under
  `/api/systems/{system}/games/{game}/uploads/{upload}`. Nothing is
  addressed by its hash.

Every route describes a file with the same object:

```json
{"id":17,"system":"system01","game":"game01","turn":1,"player":5,
 "name":"report.txt","size":2048,
 "sha256":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
 "uploaded_by":"acct01@example.com","uploaded_at":"2026-11-29T12:00:05.000000000Z",
 "withdrawn_at":null}
```

- `turn` is `null` for a file at the game's level. `player` is `null`
  unless the file is in a player's folder.
- `size` is in bytes and `sha256` is the lowercase hex SHA-256 of the
  file, both of exactly the bytes uploaded.
- `uploaded_by` is the uploader's email as it was at the time.
- `withdrawn_at` is `null` unless the file was withdrawn.

### `POST .../files?name=NAME`

Uploads one file to the folder. The body is the file itself, not JSON and
not a form, with `Content-Type: text/plain` or `application/octet-stream`.
The file's name goes in the `name` query parameter.

```sh
curl -s "$FUINGO/api/systems/system01/games/game01/turns/1/players/5/files?name=report.txt" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: text/plain' \
  --data-binary @report.txt
```

Use `--data-binary`, not `-d`: `-d` strips line endings. The response is
the file's object, with its new `id`.

| Status | When |
| --- | --- |
| `201` | Stored. |
| `400` | `name` is missing or not allowed, or the file is empty, not valid UTF-8, or holds a forbidden control character. The message says which. |
| `404` | The folder does not exist. |
| `413` | The file is larger than the limit. The message gives the limit in bytes. |
| `415` | `Content-Type` is not `text/plain` or `application/octet-stream`. |

- **Names** are 1 to 48 ASCII characters: letters, digits, `-`, `_`, and
  `.`, with no `.` at the start or end. No spaces. Case is kept. The server
  never interprets a name.
- **Files are text.** A file may start with one UTF-8 byte order mark and
  must then be valid UTF-8 with at least one byte. The only control
  characters allowed are tab, line feed, and carriage return. The whole
  file is checked.
- **Size.** At most 1 MiB (1,048,576 bytes) unless the server is
  configured otherwise.
- **Stored as sent.** The server keeps the exact bytes, including a byte
  order mark and the line endings.
- An upload is not safe to retry blindly: a second request stores a second
  file. After a timeout, list the folder and compare `sha256` first.

### `GET .../files`

Lists the files in exactly that folder, not in the folders under it, in the
order they arrived.

```sh
curl -s "$FUINGO/api/systems/system01/games/game01/turns/1/players/5/files" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"system":"system01","game":"game01","turn":1,"player":5,"files":[ ... ]}
```

- Withdrawn files are left out. Add `?withdrawn=include` to list them too;
  any other value gets `400`.
- `files` is `[]`, never `null`, for an empty folder.

### `GET /api/systems/{system}/games/{game}/uploads`

Lists the game's files at every level, in order of upload ID. This is how
a tool pulls a turn's orders, or polls for new ones.

```sh
curl -s "$FUINGO/api/systems/system01/games/game01/uploads?after=17" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"system":"system01","game":"game01","uploads":[ ... ]}
```

- `?after=ID` gives only the uploads with a greater ID. Remember the
  highest `id` you have handled and send it next time; `0` or no `after`
  gives everything. IDs increase in the order uploads arrive, so nothing
  appears later with a lower ID. A value that is not a whole number, 0 or
  more, gets `400`.
- `?withdrawn=include` works as for a folder. Without it, a file withdrawn
  after you saw it simply stops being listed.
- Filter by `turn` and `player` in the client. Upload IDs are shared by all
  games, so a game's IDs have gaps.

### `GET /api/systems/{system}/games/{game}/uploads/{upload}`

Returns one file's object, withdrawn or not.

### `GET /api/systems/{system}/games/{game}/uploads/{upload}/contents`

Downloads the file: exactly the bytes that were uploaded.

```sh
curl -s -o report.txt "$FUINGO/api/systems/system01/games/game01/uploads/17/contents" \
  -H "Authorization: Bearer $TOKEN"
```

- `X-Content-SHA256` holds the file's lowercase hex SHA-256. Check it.
- The response is `Content-Type: application/octet-stream` with
  `Content-Disposition: attachment` and the file's name, whatever the file
  holds, so a browser never displays it.
- A withdrawn file can still be downloaded by its ID.
- There is no normalization yet: no option removes a byte order mark or
  changes line endings.

### `POST /api/systems/{system}/games/{game}/uploads/{upload}/withdraw`

Withdraws a file the calling account uploaded, for example a report put in
the wrong folder. Send no body. The response is the file's object.

| Status | When |
| --- | --- |
| `200` | Withdrawn. Repeating the call succeeds and keeps the first `withdrawn_at`. |
| `403` | Another account uploaded the file, even another GM of the game. |
| `404` | The game has no such upload. |

Withdrawing hides the file from listings. It deletes nothing, and it
cannot be undone: upload the file again instead.

## Local development

In the `development` and `agent` environments only, a new database starts
with a default system, games, players, and accounts, and the server has
helper routes that skip the password. None of this exists in `test` or
`production`. [TESTING.md](TESTING.md) is the full reference.

The default system is `system01`. Default accounts with passwords:

| Email | Password | Manages |
| --- | --- | --- |
| `acct01@example.com` | `worser-choices-2001` | `system01/game01` |
| `acct02@example.com` | `worser-choices-2002` | `system01/game02` |

Default players, whose accounts have no password, are `catbird@example.com`
and `spoonbill@example.com` in `game01` and `caracara@example.com` in
`game02`.

To run a throwaway server with those defaults and an in-memory database:

```sh
go run ./cmd/fuingod --env agent --db :memory:
```

It listens on `127.0.0.1:18443`. A command-line client may call that
address over plain HTTP directly; browsers should use the HTTPS origin.

Helper routes, under `/__development` in `development` and `/__agent` in
`agent`. Each requires `POST`:

| Route | Does |
| --- | --- |
| `POST /__agent/log-me-in/{email}` | Returns a bearer token for an existing account with a password, in the same JSON as `/api/login`, without checking the password. |
| `POST /__agent/create-api-account/{email}` | Creates an account with a random password and returns the password as plain text (`201`). It manages no games. |

Helper errors are plain text, not JSON.

## Writing a client

- Hold one token and reuse it until a route returns `401`, then log in once
  and retry the request once. Do not log in per request: each login is a
  bcrypt check and a new session row.
- On `429`, wait `Retry-After` seconds. On `500`, back off and retry.
- Treat `404` on a game as "not mine or not there". Retrying will not help.
- Send no body on `disable`, `enable`, `close`, `open`, and `withdraw`. One sent is
  ignored, not validated.
- After a timeout on `POST .../turns`, send the same request again: `409`
  means the first call created the turn.
- After a timeout on `POST .../players`, list the players before retrying:
  the first call may have succeeded, and a retry then gets `409`.
- Ignore JSON fields you do not recognize in responses, so the client
  survives additions.
- Require `https` except for `localhost` and loopback addresses.
