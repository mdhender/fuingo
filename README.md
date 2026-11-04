# fuingo

A web server that accepts turn-file uploads for play-by-email games. Players
upload a turn file through a web form instead of mailing it. The file's first
line, `<game code> <turn number> <email address> <token>`, authorizes it; the
server stores the rest of the file. Game managers read the uploads through
the `/api/` routes, and `olympia` stages them as orders for the Olympia G3
engine.

## Commands

```sh
go run ./cmd/fuingodb --env development create --db tmp/fuingo.db
go run ./cmd/fuingodb --env development migrate --db tmp/fuingo.db
go run ./cmd/fuingod  --env development --db tmp/fuingo.db
go run ./cmd/fuingo   version
go run ./cmd/fuingo   --env development --db tmp/fuingo.db version --schema
go run ./cmd/fuingo   --env development --db tmp/fuingo.db game create game03
go run ./cmd/fuingo   --env development --db tmp/fuingo.db game add --uploader tern@example.com --code game03
go run ./cmd/fuingo   --env development --db tmp/fuingo.db account create --generate-password acct03@example.com
go run ./cmd/fuingo   --env development --db tmp/fuingo.db game add --manager acct03@example.com --code game03
go run ./cmd/olympia  --env development stage --game game01 --turn 1
```

`fuingodb` creates and migrates databases. `fuingod` serves the upload form and
only opens an existing, current database; it never creates or migrates one.
`fuingo` is the operators' CLI for the application data. `olympia` is the game
master's tool for Olympia G3 games; see [olympia](#olympia). `fuingo version`
prints the program's version; `fuingo version --schema` prints the database's
schema version. Commands that use the database, like `fuingod`, refuse one
that is not at the schema version the program expects. `fuingo` never creates
or migrates a database file; use `fuingodb` for that.

| Command | Does |
| --- | --- |
| `fuingo game create CODE` | Adds an active game. |
| `fuingo game add --manager EMAIL --code CODE` | Makes an existing account a manager of an existing game. |
| `fuingo game add --uploader EMAIL --code CODE [--token TOKEN]` | Adds an active uploader to an existing game, creating the account (without a password) if needed. Without `--token`, generates a random UUID token and prints it once; send it to the uploader, because it cannot be recovered. A given token is never printed. |
| `fuingo game disable --code CODE` | Disables a game: it refuses every upload. `game enable` reverses it. |
| `fuingo game disable --uploader EMAIL --code CODE` | Disables an uploader in a game: their uploads are refused. `game enable` reverses it. |
| `fuingo game disable --manager EMAIL --code CODE` | Stops an account managing a game. `game enable` reverses it. |
| `fuingo account create [--generate-password] EMAIL` | Adds an account with a password, which the API requires. Fails if the email already has an account, including one made by `game add --uploader`; give that one a password with `account set-password`. Reads the password from the first line of standard input (`printf '%s\n' "$PW" \| fuingo ... account create EMAIL`), or with `--generate-password` generates one and prints it once. At most 72 bytes. |
| `fuingo account set-password [--generate-password] EMAIL` | Sets or replaces the password of an existing account, such as one made by `game add --uploader`. Reads or generates the password like `account create`. |
| `fuingo account disable EMAIL` | Disables an account: it manages no games, cannot authenticate, and cannot upload in any game. `account enable` reverses it. |

Game codes and emails are lowercased, like the ones in uploaded files. Game
codes, emails, and tokens must be one or more printable ASCII bytes with no
spaces.

## Configuration

Every flag can be set with an environment variable instead. Explicit flags
override environment variables, which override the defaults.

| Flag | Environment variable | Commands | Meaning |
| --- | --- | --- | --- |
| `--env` | `FUINGO_ENV` | all | `development`, `test`, `production`, or `agent`. Required, except by `fuingo version` without `--schema`. |
| `--db` | `FUINGO_DB` | all | Database file. Required. `fuingod` accepts `:memory:` in `development` and `agent` only; `fuingodb` and `fuingo` never do. `fuingo version` uses it only with `--schema`. |
| `--addr` | `FUINGO_ADDR` | `fuingod` | Backend listen address, `host:port`. Default `127.0.0.1:18443`. |
| `--trusted-proxies` | `FUINGO_TRUSTED_PROXIES` | `fuingod` | Comma-separated addresses and CIDR prefixes of the reverse proxies whose `X-Forwarded-For` header is trusted. Default `127.0.0.0/8,::1` (loopback); empty trusts none. |
| `--schema` | (none) | `fuingo version` | Print the database's schema version. Command line only. |
| `--token` | (none) | `fuingo game add --uploader` | The new uploader's token. Command line only. |
| `--generate-password` | (none) | `fuingo account create`, `set-password` | Generate the password instead of reading it. Command line only. |
| `--manager`, `--uploader`, `--code` | (none) | `fuingo game add`, `disable`, `enable` | The manager's or uploader's email, and the game's code. `--code` is required; `game add` needs exactly one of the others, and `disable` and `enable` at most one. Command line only. |
| (none) | `FUINGO_PEPPER` | `fuingod`, `fuingodb create`, `fuingo game add --uploader` | Key for the upload credential hashes. Required. |

- The environment is read from `--env` or the process's `FUINGO_ENV`. The
  commands then load that environment's files from the working directory, in
  this order of precedence: the process environment, `.env.<env>.local`,
  `.env.local`, `.env.<env>`, `.env`.
- Flags marked "command line only" ignore their `FUINGO_` variables, so an
  environment file cannot change what a command does.
- `FUINGO_PEPPER` is a secret and has no flag. Keep it in the process
  environment or an ignored `.local` file, never in `.env` or `.env.<env>`.
  Changing it invalidates every stored upload token.
- `fuingodb create` in `development` and `agent` seeds the default games,
  uploaders, and managers listed in [TESTING.md](TESTING.md). So does
  `fuingod --db :memory:`.
- Uploaded files are stored in the database, in `upload_blobs`, keyed by the
  SHA-256 of their contents; identical files are stored once. Each successful
  upload is recorded in the `uploads` table. Nothing is written to the
  filesystem, so backing up the database backs up every upload.

## API

`fuingod` serves these routes under `/api/` for game managers. Requests and
responses are JSON, except a body download. Every response has
`Cache-Control: no-store`.

| Route | Does |
| --- | --- |
| `POST /api/login` | Takes `{"email": ..., "password": ...}` with `Content-Type: application/json`. Returns `{"token": ..., "expires_at": ...}`. The token is valid for 24 hours. |
| `GET /api/games/{code}/uploads[?turn=N]` | Lists the game's uploads, oldest first: `{"game": ..., "uploads": [{"id", "email", "turn", "content_hash", "size", "uploaded_at"}]}`. `turn` matches the turn number exactly as written in the uploaded file. |
| `GET /api/games/{code}/uploads/{id}/body` | Returns an upload's contents (without its header line), with the SHA-256 in `X-Content-SHA256`. |

- Log in with an account that has a password (`fuingo account create` or
  `account set-password`). Send the token as `Authorization: Bearer TOKEN`.
  Only the token's SHA-256 is stored. An upload token never logs in, and an
  API password never authorizes an upload.
- Every failed login gets the same `401`, whether the email is unknown, the
  password is wrong, or the account is disabled or has no password. Failed
  logins are limited per client address like failed uploads: 5, then one per
  minute, with `429` and `Retry-After` beyond that.
- An account sees only the games it manages while it and its
  `game_managers` row are active, whether or not the game is. Any other game,
  and any upload outside a managed game, gets the same `404` as one that does
  not exist. Upload contents are reachable only through an upload, never by
  content hash. A disabled account's tokens stop working at once.
- Timestamps are UTC, as `2006-01-02T15:04:05.000000000Z`.
- There is no pagination yet, and no route changes any data.

## olympia

`olympia stage` downloads every upload for one turn of a game and writes them
to the Olympia G3 engine's spool directory as order files.

```sh
export OLYMPIA_SERVER=https://htmx-app.localhost:8443
export OLYMPIA_EMAIL=acct01@example.com
export OLYMPIA_SPOOL=~/Games/olympia-g3/alpha/lib/spool
export OLYMPIA_BATCH=~/Games/olympia-g3/alpha/batches
# OLYMPIA_PASSWORD goes in the process environment or an ignored .local file.
olympia --env production stage --game alpha --turn 3
```

1. It logs in to fuingo with `OLYMPIA_EMAIL` and `OLYMPIA_PASSWORD`, an
   account that manages the game.
2. It downloads the turn's uploads into a batch directory,
   `<batch>/<game>/turn-<turn>/`: `blobs/<sha256>` for each distinct body and
   `manifest.json` listing, for each file to stage, its name, the upload ID,
   the uploader's email, the upload time, the content hash, the size, and any
   lines over 1,023 bytes (which the engine truncates). The batch appears only
   once every body matches its hash; a failed or interrupted download leaves
   nothing behind. A turn with no uploads is an error.
3. It writes one file per upload to the spool, in upload order, named
   `m<turn>-<seq>-<email>-<upload id>`, such as
   `m3-0002-alice-example-com-17`:

   ```text
   From alice@example.com Wed Nov  4 07:36:00 2026
   From: alice@example.com

   <the uploaded orders, with LF line endings>
   ```

   Every upload for the turn is staged, not only each player's latest, and
   the engine applies them one at a time, so feed them to it in name order.

Once a turn's batch exists it is frozen: staging the turn again, for example
after the engine has consumed the spool, reuses it without logging in, and
ignores later uploads. `--rebuild` downloads it again. `olympia` refuses a
spool directory that already holds files starting with `m`, since the engine
would read them as orders, unless given `--force`; it never deletes them.

`olympia` never writes the password, its bearer token, or upload tokens to
any file. It requires `https`, except for `localhost` and loopback addresses.

| Flag | Environment variable | Meaning |
| --- | --- | --- |
| `--env` | `OLYMPIA_ENV` | `development`, `test`, `production`, or `agent`; selects the environment files, as for the other commands. Required by `stage`. |
| `--server` | `OLYMPIA_SERVER` | fuingo's URL. Required. |
| `--email` | `OLYMPIA_EMAIL` | The email of an account that manages the game. Required. |
| `--spool` | `OLYMPIA_SPOOL` | The engine's spool directory, `<libdir>/spool`. Must exist. Required. |
| `--batch` | `OLYMPIA_BATCH` | The directory holding the batches. Must exist. Required. |
| `--game`, `--turn` | (none) | The game code and the turn number (decimal digits). Required. Command line only. |
| `--rebuild` | (none) | Download the batch again even if it exists. Command line only. |
| `--force` | (none) | Stage even if the spool holds files starting with `m`. Command line only. |
| (none) | `OLYMPIA_PASSWORD` | The account's API password. Required. |

Not yet implemented: managing games, players, turns, and orders in
`olympia`, replacement and cutoff rules for multiple submissions, and
running the engine or mailing reports, which stay with the GM's scripts.

## Proxy

`fuingod` serves plain HTTP and expects a TLS-terminating reverse proxy that
preserves `Host`, `Origin`, and `Sec-Fetch-Site`. In development, Caddy serves
`https://htmx-app.localhost:8443` and forwards to `127.0.0.1:18443`. See
[PROXIES.md](PROXIES.md).

### Client addresses and failed uploads

Failed upload authorizations are limited per client address: each address
may fail 5 times, then once more per minute, and gets `429 Too Many Requests`
while over the limit. IPv6 clients are grouped by /64. The limit applies to
the address, not the email or game, so guesses from one address never lock an
uploader out elsewhere. Limits are kept in memory and reset on restart. At
most 16,384 addresses are tracked; when the table is full, a new address
replaces the one that has regained the most attempts.

Behind a proxy, every connection comes from the proxy, so `fuingod` must learn
the real client address from the proxy's `X-Forwarded-For` header. Set this up
for each deployment:

- **Tell `fuingod` which proxies to trust.** Set `FUINGO_TRUSTED_PROXIES` to
  the addresses the proxy connects from. The default, loopback, is right when
  Caddy runs on the same host. If Caddy runs elsewhere, list its address (for
  example `FUINGO_TRUSTED_PROXIES=10.0.0.5`); otherwise every upload appears
  to come from Caddy, and one guesser's failures limit everyone.
- **Make sure the proxy sends `X-Forwarded-For`.** Caddy's `reverse_proxy`
  does this by default, replacing any value sent by an untrusted client, so
  no Caddyfile change is needed. Do not configure Caddy to pass through a
  client's own `X-Forwarded-For` unless the hop in front of it is also trusted
  (Caddy's `trusted_proxies` option).
- **If another proxy or load balancer sits in front of Caddy**, add its
  addresses to both Caddy's `trusted_proxies` and `FUINGO_TRUSTED_PROXIES`.
  `fuingod` reads the header from the right and uses the first address that is
  not a trusted proxy; addresses to its left were supplied by the client and
  are ignored.
- **Keep the backend unreachable except through the proxy.** Anyone who can
  connect from a trusted address can claim any client address.

## Running as a systemd service

[contrib/systemd/fuingod.service](contrib/systemd/fuingod.service) runs
`fuingod` as the `fuingo` user, behind Caddy on the same host. It reads the
database from `/var/lib/fuingo/fuingo.db`, listens on `127.0.0.1:18443`, and
gets `FUINGO_PEPPER` from `/etc/fuingo/fuingod.env`.

1. Build the binaries. The SQLite driver is pure Go, so they cross-compile:

   ```sh
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tmp/linux/ ./cmd/fuingod ./cmd/fuingodb ./cmd/fuingo
   ```

   Copy them to `/usr/local/bin` on the server.

2. Create the user, the database directory, and the environment file. The
   pepper must never change once uploaders have tokens; keep a copy somewhere
   safe.

   ```sh
   sudo useradd --system --home-dir /var/lib/fuingo --shell /usr/sbin/nologin fuingo
   sudo install -d -o fuingo -g fuingo -m 0750 /var/lib/fuingo
   sudo install -d -m 0755 /etc/fuingo
   printf 'FUINGO_PEPPER=%s\n' "$(openssl rand -hex 32)" | sudo tee /etc/fuingo/fuingod.env >/dev/null
   sudo chown root:fuingo /etc/fuingo/fuingod.env
   sudo chmod 0640 /etc/fuingo/fuingod.env
   ```

3. Create the database as the `fuingo` user, so it owns the file. In
   production it starts empty: no default games or accounts. The commands
   look for `.env` files in the current directory and fail if they cannot
   read it, so run them from `/var/lib/fuingo`.

   ```sh
   sudo -u fuingo sh -c 'cd /var/lib/fuingo; set -a; . /etc/fuingo/fuingod.env; exec fuingodb --env production create --db /var/lib/fuingo/fuingo.db'
   ```

4. Install and start the service:

   ```sh
   sudo cp contrib/systemd/fuingod.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now fuingod
   journalctl -u fuingod -f
   ```

5. Point Caddy at it. Caddy obtains the certificate for the domain itself:

   ```caddyfile
   uploads.example.com {
   	reverse_proxy 127.0.0.1:18443
   }
   ```

Run `fuingo` as the `fuingo` user with the same environment, for example:

```sh
sudo -u fuingo sh -c 'cd /var/lib/fuingo; set -a; . /etc/fuingo/fuingod.env; exec fuingo --env production --db /var/lib/fuingo/fuingo.db game create game01'
```

On `systemctl stop` or `restart`, systemd sends SIGTERM. `fuingod` stops
accepting connections, gives in-flight requests 10 seconds to finish, cuts off
any still running, closes the database, and exits; it exits non-zero if it
had to cut requests off. A second SIGTERM or Ctrl-C stops it at once. The
unit waits 30 seconds before killing it. Even a killed server loses no
completed upload: each one is committed before its response is sent.

Each start runs `fuingodb migrate` first, so deploying a new version is
copying the binaries and `sudo systemctl restart fuingod`. Back up the live
database with `sqlite3 /var/lib/fuingo/fuingo.db ".backup /path/to/backup.db"`
or `VACUUM INTO`, never by copying the file while the server runs.
