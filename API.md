# The fuingo API

Reference for anyone, human or agent, writing a tool that calls `fuingod`'s
`/api/` routes. It describes what a client sends and receives. For the rules
that govern changing the server, see [AGENTS.md](AGENTS.md).

The API is for **game managers**: it creates games, manages a game's
uploaders and their upload tokens, and reads the turn files players have
uploaded. Players do not use it; they upload turn files through the web
form (see [Uploading a turn file](#uploading-a-turn-file)).

## Quick start

```sh
FUINGO=https://htmx-app.localhost:8443   # the development origin

# 1. Log in. The token lasts 24 hours.
TOKEN=$(curl -s "$FUINGO/api/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"acct01@example.com","password":"worser-choices-2001"}' \
  | jq -r .token)

# 2. Call a route with the token.
curl -s "$FUINGO/api/games/game01/uploads?turn=7" \
  -H "Authorization: Bearer $TOKEN"
```

The email and password above are one of the public default accounts that
exist only in `development` and `agent`; see
[Local development](#local-development).

## Setting up a game

A logged-in account can create a game and add its players without help
from an operator. Continuing from the quick start:

```sh
# Create the game. The caller becomes its manager.
curl -s "$FUINGO/api/games" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"code":"alpha"}'

# Add each player. Every response carries that player's upload token,
# which is shown only once, so save the output.
for n in 1 2 3; do
  curl -s "$FUINGO/api/games/alpha/uploaders" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d "{\"email\":\"player-$n@example.com\"}"
  echo
done

# Check the result.
curl -s "$FUINGO/api/games/alpha/uploaders" -H "Authorization: Bearer $TOKEN"
```

Send each player the game code and their token; they put both on the
first line of every turn file (see
[Uploading a turn file](#uploading-a-turn-file)). The account itself, with
its password, must come from an operator: see "Creating accounts, games,
and players on the server" in [README.md](README.md).

## Conventions

- **Base URL.** Every route is under `/api/` on the server's public HTTPS
  origin. In development that is `https://htmx-app.localhost:8443`.
- **Requests.** A request with a body sends a JSON object with
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
  successful download of an upload's body, which is `text/plain`. Every
  error is JSON. Every response has `Cache-Control: no-store`.
- **Game codes and emails** are case-insensitive: the server lowercases
  them, and returns them lowercased. Each must be one or more printable
  ASCII characters (`0x21`–`0x7E`), with no spaces.
- **IDs** are positive integers, never reused. Uploaders and uploads are
  addressed by ID in paths, never by email.
- **Timestamps** are UTC, always in the form
  `2026-11-21T22:06:29.628072000Z` (nine fractional digits), so they sort as
  text.
- **Browsers.** The routes sit behind cross-origin protection. A
  command-line client, which sends neither `Origin` nor `Sec-Fetch-Site`,
  passes. A request from a page on another origin gets `403`. There is no
  CORS support.
- **No pagination.** List routes return everything.
- **Nothing is deleted.** No route deletes a game, uploader, or upload.
  Revoke access by disabling.

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
| `400` | The body is not exactly one valid JSON object, has an unknown or badly formed field, or a value breaks a rule above. |
| `401` | Login failed, or the bearer token is missing, unknown, or expired. On routes other than login, the response has `WWW-Authenticate: Bearer realm="fuingo"`. Log in again. |
| `403` | Cross-origin request refused. |
| `404` | The game or item does not exist, or the account does not manage the game. The two cases are deliberately identical, and identical to an unknown path under `/api/`. |
| `405` | Wrong method for the path. The `Allow` header lists the methods it accepts. |
| `409` | The game code or uploader already exists. |
| `415` | A body was required and `Content-Type` was not `application/json`. |
| `429` | Too many failed logins from this client address. `Retry-After` gives the seconds to wait. |
| `500` | Server error. Safe to retry later. |

Checks run in a fixed order, and the first failure decides the status:

1. `403` for a cross-origin request.
2. `404` for an unknown path, or `405` for a wrong method.
3. `401` for a missing or invalid token (except on login).
4. `404` for a game the account does not manage.
5. `415`, then `400`, for the body.
6. `404` or `409` for the item itself.

So the first two do not depend on being logged in. A request with no
token gets `401`, never `415` or `400`, whatever its body. A bad body sent
to a game the account does not manage gets `404`, not `400`. On
`POST /api/games`, which names no game in its path, step 4 does not apply.
On login, the order is `415`, `400`, `429`, then `401`.

A proxy in
front of the server may still answer in its own format when the server is
down (for example `502`), so a robust client checks `Content-Type` before
parsing an error.

## Authentication

### `POST /api/login`

Exchanges an account's email and API password for a bearer token.

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
- **Changing the account's password does not end its tokens.** A token
  issued under the old password stays valid until it expires. Disabling
  the account is the only way to cut one off early.
- Every failed login gets the same `401`, whether the email is unknown, the
  password is wrong, the account is disabled, or the account has no
  password. A client cannot tell these apart.
- **Failure limit.** Each client address may fail 5 times, then once more
  per minute. Beyond that every login from the address gets `429`, even
  with the right password, until `Retry-After` seconds pass. Successful
  logins and malformed requests (`400`, `415`) do not count. Do not retry
  a `401` in a loop. IPv6 clients are grouped by /64 prefix, so tools on
  one network may share a limit. The limit is separate from the one on
  failed uploads.
- Only an account with an API password can log in. An operator creates one
  with `fuingo account create` or `fuingo account set-password`; the API
  cannot.
- An **upload token is not an API password**, and the reverse. An upload
  token never logs in here, and an API password never authorizes an upload.

Keep the password and token out of logs, files, and command lines that
other users can read.

## What an account can reach

An account acts only on the games it **manages**. It manages a game it
created with `POST /api/games`, or one an operator added it to with
`fuingo game add --manager`.

- For any other game, every route with a game code in its path returns
  the same `404` as for a game that does not exist. So does an uploader or upload ID that belongs to
  another game, even when requested through a game the account manages.
- A disabled game is still fully manageable. Disabling a game only stops
  uploads to it.
- If an operator disables the account's manager row for a game, the
  account gets `404` for that game.
- `GET /api/games` lists the games the account can reach, and
  `POST /api/games` reports `409` for a code that is taken by anyone. That
  `409` is the one place the API reveals a game the account does not
  manage.

## Games

### `GET /api/games`

Lists the games the caller manages, in order of code.

```sh
curl -s "$FUINGO/api/games" -H "Authorization: Bearer $TOKEN"
```

```json
{"games":[{"code":"alpha","active":false},{"code":"game01","active":true}]}
```

- `active` is whether the game accepts uploads. An inactive game is still
  listed and still manageable.
- A game whose manager row an operator has disabled is not listed.
- `games` is `[]`, never `null`, for an account that manages none.

### `POST /api/games`

Creates an active game and makes the caller its manager.

```sh
curl -s "$FUINGO/api/games" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"code":"Alpha"}'
```

```json
{"game":"alpha"}
```

| Status | When |
| --- | --- |
| `201` | Created. `game` is the lowercased code. |
| `400` | `code` is missing, empty, or not printable ASCII without spaces. |
| `409` | A game with that code exists, whoever manages it. |

Any account that can log in may create games. No route renames, disables,
or deletes a game, or adds another manager; an operator does those with
`fuingo`.

## Uploaders

An uploader is one email's permission to upload turn files to one game. It
has its own upload token for that game. The same email in two games is two
uploaders, with different IDs and tokens.

### `GET /api/games/{code}/uploaders`

Lists the game's uploaders, enabled or not, in order of ID.

```sh
curl -s "$FUINGO/api/games/game01/uploaders" -H "Authorization: Bearer $TOKEN"
```

```json
{"game":"game01","uploaders":[
  {"id":1,"email":"catbird@example.com","active":true,"account_active":true},
  {"id":2,"email":"spoonbill@example.com","active":true,"account_active":true}
]}
```

- `active` is the uploader's own flag in this game, which the `disable` and
  `enable` routes change.
- `account_active` is the flag on the email's account, which covers every
  game. Only an operator changes it.
- An uploader can upload only while `active`, `account_active`, and the
  game are all active.
- `uploaders` is `[]`, never `null`, for a game with none.
- Tokens are never listed. They cannot be read back at all.

### `POST /api/games/{code}/uploaders`

Adds an enabled uploader to the game and returns its upload token.

```sh
curl -s "$FUINGO/api/games/game01/uploaders" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"email":"Wren@Example.com"}'
```

```json
{"game":"game01","id":4,"email":"wren@example.com","token":"3f0e6c1a-7b0d-4f6e-9a55-0d7c1e2b9a41"}
```

| Status | When |
| --- | --- |
| `201` | Added. |
| `400` | `email` is missing, empty, or not printable ASCII without spaces. |
| `404` | The account does not manage the game. |
| `409` | The email is already an uploader of the game, even a disabled one. Enable it or reset its token instead. |

- **The token appears only in this response.** The server keeps only a
  hash. Deliver it to the player, with the game code, out of band. If it is
  lost, reset it.
- The server chooses the token, a random UUID. A request cannot supply one;
  a `token` field is an unknown field and gets `400`.
- If the email has no account, one is created without an API password. It
  can upload but can never log in to the API.
- The server does not check that the email is a deliverable address.

### `POST /api/games/{code}/uploaders/{id}/disable`
### `POST /api/games/{code}/uploaders/{id}/enable`

Clears or sets the uploader's `active` flag. Neither takes a body.

```sh
curl -s -X POST "$FUINGO/api/games/game01/uploaders/4/disable" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"game":"game01","id":4,"active":false}
```

| Status | When |
| --- | --- |
| `200` | Done. Repeating the call succeeds and changes nothing. |
| `404` | The account does not manage the game, or the game has no uploader with that ID. |

A disabled uploader's uploads are refused at once. Its token and its past
uploads are kept, so enabling it restores the same token.

### `POST /api/games/{code}/uploaders/{id}/reset-token`

Replaces the uploader's upload token. Takes no body.

```sh
curl -s -X POST "$FUINGO/api/games/game01/uploaders/4/reset-token" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{"game":"game01","id":4,"email":"wren@example.com","token":"b8a3d1f2-64c7-4e0b-8d39-52f7a9c0e6d4"}
```

| Status | When |
| --- | --- |
| `200` | Reset. `token` is the new token, shown only here. |
| `404` | The account does not manage the game, or the game has no uploader with that ID. |

- The old token stops working at once. This call is **not idempotent**:
  every call issues a new token and invalidates the last.
- It does not change `active`. Resetting a disabled uploader leaves it
  disabled.
- The uploader's ID and upload history are unchanged.

## Uploads

An upload is one turn file a player submitted. Every successful submission
is kept as its own upload, including a repeat of the same contents and
several submissions for one turn. The API does not pick a "latest" or
"final" one; the client decides.

### `GET /api/games/{code}/uploads`

Lists the game's uploads, oldest first. Add `?turn=N` to list one turn.

```sh
curl -s "$FUINGO/api/games/game01/uploads?turn=7" -H "Authorization: Bearer $TOKEN"
```

```json
{"game":"game01","uploads":[
  {"id":1,"game":"game01","email":"catbird@example.com","turn":"7",
   "content_hash":"2cb0fbbb2a297e85dec6b2bd00b0057c16dbbc7098ac827b04e8c04c285e712b",
   "size":10,"uploaded_at":"2026-11-21T22:06:29.628072000Z"}
]}
```

| Field | Meaning |
| --- | --- |
| `id` | The upload's ID, for the body route. |
| `game` | The game's code, the same as the top-level `game`. |
| `email` | The uploader's email when the upload arrived. |
| `turn` | The turn number as the player wrote it in the file. |
| `content_hash` | Lowercase hex SHA-256 of the body. |
| `size` | The body's length in bytes. |
| `uploaded_at` | When the upload arrived. |

| Status | When |
| --- | --- |
| `200` | Listed. `uploads` is `[]`, never `null`, when there are none. |
| `400` | `turn` is present but empty. |
| `404` | The account does not manage the game. |

- **`turn` is text, matched exactly.** `?turn=7` does not match an upload
  whose file said `07` or `7.0`. A turn is at most 32 printable ASCII
  characters and need not be a number.
- Two uploads with the same `content_hash` have identical bodies; a client
  may download one and reuse it.
- The list has no uploader ID. Match uploads to uploaders by `email`.

### `GET /api/games/{code}/uploads/{id}/body`

Returns one upload's contents.

```sh
curl -s -D - "$FUINGO/api/games/game01/uploads/1/body" -H "Authorization: Bearer $TOKEN"
```

```text
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
Content-Length: 10
X-Content-SHA256: 2cb0fbbb2a297e85dec6b2bd00b0057c16dbbc7098ac827b04e8c04c285e712b

UNIT 1234
```

| Status | When |
| --- | --- |
| `200` | The body follows. |
| `404` | The account does not manage the game, or the game has no upload with that ID. |

- The body is the file **without its first line**, the header line that
  carried the upload token, and without a leading byte-order mark. The
  token is never stored or returned.
- The bytes are otherwise exactly as uploaded. Line endings may be LF or
  CRLF, the last line may lack a newline, and only the first 256 bytes of
  the original file were checked to be ASCII text. Despite the
  `charset=utf-8` label, treat the body as bytes.
- A body is at most 65,536 bytes.
- `X-Content-SHA256` equals the upload's `content_hash`. Verify the bytes
  received against it.
- There is no route that fetches a body by its hash.

## Uploading a turn file

Uploading is not part of `/api/` and needs no login. It is described here
because a manager's tool hands out the tokens that make it work.

A player uploads a `.txt` file through the form at `/`, which posts it to
`POST /upload` as `multipart/form-data` in a field named `file`. The file's
first line is four words: the game code, the turn, the uploader's email,
and that uploader's token for the game. The words are separated by one or
more spaces or tabs, and the line may end in LF or CRLF:

```text
alpha 7 wren@example.com 3f0e6c1a-7b0d-4f6e-9a55-0d7c1e2b9a41
<the orders>
```

Everything after the first line is the body the API returns. The game code
and email are matched case-insensitively; the token is not.

The response is an HTML fragment for the form, not JSON. `200` means the
file was stored; the message is a receipt with the body's size and SHA-256.
Failed authorizations are limited per client address in the same way as
failed logins. [README.md](README.md) and [AGENTS.md](AGENTS.md) give the
full rules for the file.

## Local development

In the `development` and `agent` environments only, a new database starts
with default games and accounts, and the server has helper routes that skip
the password. None of this exists in `test` or `production`.
[TESTING.md](TESTING.md) is the full reference.

Default API accounts:

| Email | Password | Manages |
| --- | --- | --- |
| `acct01@example.com` | `worser-choices-2001` | `game01` |
| `acct02@example.com` | `worser-choices-2002` | `game02` |

Default uploaders are `catbird@example.com` and `spoonbill@example.com` in
`game01` and `caracara@example.com` in `game02`; their tokens are in
[TESTING.md](TESTING.md).

To run a throwaway server with those defaults and an in-memory database:

```sh
FUINGO_PEPPER=any-test-value go run ./cmd/fuingod --env agent --db :memory:
```

It listens on `127.0.0.1:18443`. A command-line client may call that
address over plain HTTP directly; browsers should use the HTTPS origin.

Helper routes, under `/__development` in `development` and `/__agent` in
`agent`. Each requires `POST`:

| Route | Does |
| --- | --- |
| `POST /__agent/log-me-in/{email}` | Returns a bearer token for an existing account with a password, in the same JSON as `/api/login`, without checking the password. |
| `POST /__agent/create-api-account/{email}` | Creates an account with a random password and returns the password as plain text (`201`). It manages no games until it creates one with `POST /api/games`. |
| `POST /__agent/create-account/{game}/{email}/{token}` | Adds an uploader with a token you choose to an existing game. |

Helper errors are plain text, not JSON.

## Writing a client

- Hold one token and reuse it until a route returns `401`, then log in once
  and retry the request once. Do not log in per request: each login is a
  bcrypt check and a new session row.
- On `429`, wait `Retry-After` seconds. On `500`, back off and retry.
- Treat `404` on a game as "not mine or not there". Retrying will not help.
- Send no body on `disable`, `enable`, and `reset-token`. One sent is
  ignored, not validated.
- Never retry `reset-token` or `POST .../uploaders` blindly after a timeout:
  the first call may have succeeded, and its token is then lost. List the
  uploaders to see what happened; if the uploader exists, reset its token
  to get a known one.
- Ignore JSON fields you do not recognize in responses, so the client
  survives additions.
- Require `https` except for `localhost` and loopback addresses.
