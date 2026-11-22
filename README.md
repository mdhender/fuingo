# fuingo

A file exchange for play-by-email games, replacing the e-mail: players will
upload their orders and download their reports over HTTPS, and game masters
(GMs) the reverse. [AGENTS.md](AGENTS.md) describes the design.

The design is being built in steps. What exists today is the hierarchy
without its files (systems, games, a game's GMs and players, and turns) and
the `/api/` routes that manage a game's players and turns. The server stores
no files yet.

## Commands

```sh
go run ./cmd/fuingodb --env development create --db tmp/fuingo.db
go run ./cmd/fuingodb --env development migrate --db tmp/fuingo.db
go run ./cmd/fuingod  --env development --db tmp/fuingo.db
go run ./cmd/fuingo   version
go run ./cmd/fuingo   --env development --db tmp/fuingo.db version --schema
go run ./cmd/fuingo   --env development --db tmp/fuingo.db system create olympia
go run ./cmd/fuingo   --env development --db tmp/fuingo.db game create --system olympia g3
go run ./cmd/fuingo   --env development --db tmp/fuingo.db account create --generate-password acct03@example.com
go run ./cmd/fuingo   --env development --db tmp/fuingo.db game add --manager acct03@example.com --system olympia --code g3
go run ./cmd/fuingo   --env development --db tmp/fuingo.db game add --player tern@example.com --system olympia --code g3
```

`fuingodb` creates and migrates databases. `fuingod` serves the pages and the
`/api/` routes and only opens an existing, current database; it never creates
or migrates one. `fuingo` is the administrator's CLI for the application
data. `fuingo version` prints the program's version; `fuingo version
--schema` prints the database's schema version. Commands that use the
database, like `fuingod`, refuse one that is not at the schema version the
program expects. `fuingo` never creates or migrates a database file; use
`fuingodb` for that.

A **system** is a game such as Olympia; a **game** is one instance of it,
such as Olympia / G3. A game's code is unique within its system, so commands
name a game with both `--system` and its code.

| Command | Does |
| --- | --- |
| `fuingo system create CODE` | Adds a system. |
| `fuingo game create --system SYSTEM CODE` | Adds an active game to an existing system. |
| `fuingo game add --manager EMAIL --system SYSTEM --code CODE` | Makes an existing account a GM of an existing game. |
| `fuingo game add --player EMAIL --system SYSTEM --code CODE` | Makes an account a player in an existing game, creating the account (without a password) if needed. Prints the player's number. |
| `fuingo game disable --system SYSTEM --code CODE` | Disables a game: it has no active players. Its GMs still manage it. `game enable` reverses it. |
| `fuingo game disable --player EMAIL --system SYSTEM --code CODE` | Disables a player in a game. `game enable` reverses it. |
| `fuingo game disable --manager EMAIL --system SYSTEM --code CODE` | Stops an account managing a game. `game enable` reverses it. |
| `fuingo account create [--generate-password] EMAIL` | Adds an account with a password. Fails if the email already has an account, including one made by `game add --player`; give that one a password with `account set-password`. Reads the password from the first line of standard input (`printf '%s\n' "$PW" \| fuingo ... account create EMAIL`), or with `--generate-password` generates one and prints it once. At most 72 bytes. |
| `fuingo account set-password [--generate-password] EMAIL` | Sets or replaces the password of an existing account, such as one made by `game add --player`. Reads or generates the password like `account create`. |
| `fuingo account disable EMAIL` | Disables an account: it manages no games, plays in none, and cannot log in. `account enable` reverses it. |

Codes and emails are lowercased. A code is 1 to 32 characters: letters and
digits in any script, `-`, and `_`. An email is one or more printable ASCII
bytes with no spaces. Flags come before a command's argument, as in
`game create --system olympia g3`.

`account create` and `account set-password` set a password from the command
line only until invitations by mail are built; AGENTS.md's design has no
command that sets one.

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
| `--generate-password` | (none) | `fuingo account create`, `set-password` | Generate the password instead of reading it. Command line only. |
| `--system` | (none) | `fuingo game` commands | The code of the game's system. Required. Command line only. |
| `--manager`, `--player`, `--code` | (none) | `fuingo game add`, `disable`, `enable` | The GM's or player's email, and the game's code. `--code` is required; `game add` needs exactly one of the others, and `disable` and `enable` at most one. Command line only. |

- The environment is read from `--env` or the process's `FUINGO_ENV`. The
  commands then load that environment's files from the working directory, in
  this order of precedence: the process environment, `.env.<env>.local`,
  `.env.local`, `.env.<env>`, `.env`.
- Flags marked "command line only" ignore their `FUINGO_` variables, so an
  environment file cannot change what a command does.
- No setting is a secret yet. `FUINGO_PEPPER`, which the previous design
  required, is no longer read; remove it from environment files.
- `fuingodb create` in `development` and `agent` seeds the default system,
  games, players, and accounts listed in [TESTING.md](TESTING.md). So does
  `fuingod --db :memory:`.
- Migrating a database from the previous design moves its games into a system
  named `legacy`, turns its uploaders into players, and drops its stored
  uploads.

## API

`fuingod` serves these routes under `/api/` for GMs and their tools.
[API.md](API.md) is the reference for writing a client: requests,
responses, status codes, and examples for every route.

| Route | Does |
| --- | --- |
| `POST /api/login` | Exchanges an account's email and password for a bearer token valid for 24 hours. |
| `GET /api/games` | Lists the games the account manages. |
| `GET /api/systems/{system}/games/{game}/players` | Lists the game's players. |
| `POST /api/systems/{system}/games/{game}/players` | Adds a player, creating its account if needed. |
| `POST /api/systems/{system}/games/{game}/players/{id}/disable`, `.../enable` | Clears or sets the player's active flag. |
| `GET /api/systems/{system}/games/{game}/turns` | Lists the game's turns. |
| `POST /api/systems/{system}/games/{game}/turns` | Creates the game's next turn. |
| `POST /api/systems/{system}/games/{game}/turns/{turn}/close`, `.../open` | Closes a turn, or reopens it. |

Log in with an account that has a password (`fuingo account create` or
`account set-password`). An account reaches only the games it manages.

## Proxy

`fuingod` serves plain HTTP and expects a TLS-terminating reverse proxy that
preserves `Host`, `Origin`, and `Sec-Fetch-Site`. In development, Caddy serves
`https://htmx-app.localhost:8443` and forwards to `127.0.0.1:18443`. See
[PROXIES.md](PROXIES.md).

### Client addresses and failed logins

Failed logins are limited per client address: each address may fail 5 times,
then once more per minute, and gets `429 Too Many Requests` while over the
limit. IPv6 clients are grouped by /64. The limit applies to the address, not
the email, so guesses from one address never lock an account's owner out
elsewhere. Limits are kept in memory and reset on restart. At most 16,384
addresses are tracked; when the table is full, a new address replaces the one
that has regained the most attempts.

Behind a proxy, every connection comes from the proxy, so `fuingod` must learn
the real client address from the proxy's `X-Forwarded-For` header. Set this up
for each deployment:

- **Tell `fuingod` which proxies to trust.** Set `FUINGO_TRUSTED_PROXIES` to
  the addresses the proxy connects from. The default, loopback, is right when
  Caddy runs on the same host. If Caddy runs elsewhere, list its address (for
  example `FUINGO_TRUSTED_PROXIES=10.0.0.5`); otherwise every login appears
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
database from `/var/lib/fuingo/fuingo.db` and listens on `127.0.0.1:18443`.

1. Build the binaries. The SQLite driver is pure Go, so they cross-compile:

   ```sh
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tmp/linux/ ./cmd/fuingod ./cmd/fuingodb ./cmd/fuingo
   ```

   Copy them to `/usr/local/bin` on the server.

2. Create the user and the database directory:

   ```sh
   sudo useradd --system --home-dir /var/lib/fuingo --shell /usr/sbin/nologin fuingo
   sudo install -d -o fuingo -g fuingo -m 0750 /var/lib/fuingo
   ```

3. Create the database as the `fuingo` user, so it owns the file. In
   production it starts empty: no default games or accounts. The commands
   look for `.env` files in the current directory and fail if they cannot
   read it, so run them from `/var/lib/fuingo`.

   ```sh
   sudo -u fuingo sh -c 'cd /var/lib/fuingo && exec fuingodb --env production create --db /var/lib/fuingo/fuingo.db'
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

### Creating systems, games, and accounts on the server

Run `fuingo` as the `fuingo` user, never as root, and from
`/var/lib/fuingo`:

- As root, SQLite may leave the database's `-wal` and `-shm` files owned
  by root, and the service can then no longer write to its database.
- `fuingo` looks for `.env` files in the current directory and fails if it
  cannot read it, which the `fuingo` user cannot do in root's home.

A small wrapper keeps the commands short:

```sh
fu() { sudo -u fuingo sh -c 'cd /var/lib/fuingo && exec fuingo --env production --db /var/lib/fuingo/fuingo.db "$@"' fuingo "$@"; }
```

Create the system and a game in it:

```sh
fu system create olympia
fu game create --system olympia g3
```

Create the GM's account and make it a GM of the game. The password is
printed once; only its hash is stored:

```sh
fu account create --generate-password gm@example.com
fu game add --manager gm@example.com --system olympia --code g3
```

Add the players. Each command prints that player's number:

```sh
for n in 1 2 3; do
  fu game add --player player-$n@example.com --system olympia --code g3
done
```

A GM can instead add the players through the API, without a shell on the
server; see [Players](API.md#players). Only the administrator creates
systems and games and assigns GMs.

On `systemctl stop` or `restart`, systemd sends SIGTERM. `fuingod` stops
accepting connections, gives in-flight requests 10 seconds to finish, cuts off
any still running, closes the database, and exits; it exits non-zero if it
had to cut requests off. A second SIGTERM or Ctrl-C stops it at once. The
unit waits 30 seconds before killing it. Even a killed server loses no
completed change: each one is committed before its response is sent.

Each start runs `fuingodb migrate` first, so deploying a new version is
copying the binaries and `sudo systemctl restart fuingod`. Back up the live
database with `sqlite3 /var/lib/fuingo/fuingo.db ".backup /path/to/backup.db"`
or `VACUUM INTO`, never by copying the file while the server runs.
