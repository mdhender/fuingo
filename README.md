# fuingo

A file exchange for play-by-email games, replacing the e-mail: players will
upload their orders and download their reports over HTTPS, and game masters
(GMs) the reverse. [AGENTS.md](AGENTS.md) describes the design.

The design is being built in steps. What exists today is the hierarchy
(systems, games, a game's GMs and players, and turns) and the `/api/` routes
a GM's tool uses: to manage a game's players and turns, and to upload, list,
download, and withdraw its files. Accounts are by invitation: a new account
is mailed a link to choose its password, and its owner resets a forgotten
password the same way. An account can sign in to the web, but the pages for
a game's files are not built, so only GMs exchange files, through `/api/`.

## Commands

```sh
go run ./cmd/fuingodb --env development create --db tmp/fuingo.db
go run ./cmd/fuingodb --env development migrate --db tmp/fuingo.db
go run ./cmd/fuingod  --env development --db tmp/fuingo.db
go run ./cmd/fuingo   version
go run ./cmd/fuingo   --env development --db tmp/fuingo.db version --schema
go run ./cmd/fuingo   --env development --db tmp/fuingo.db system create olympia
go run ./cmd/fuingo   --env development --db tmp/fuingo.db game create --system olympia g3
go run ./cmd/fuingo   --env development --db tmp/fuingo.db account create acct03@example.com
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
| `fuingo game add --player EMAIL --system SYSTEM --code CODE` | Makes an account a player in an existing game. Prints the player's number. If the email has no account, creates one and mails its invitation; an account that already exists is sent nothing. |
| `fuingo game disable --system SYSTEM --code CODE` | Disables a game: it has no active players. Its GMs still manage it. `game enable` reverses it. |
| `fuingo game disable --player EMAIL --system SYSTEM --code CODE` | Disables a player in a game. `game enable` reverses it. |
| `fuingo game disable --manager EMAIL --system SYSTEM --code CODE` | Stops an account managing a game. `game enable` reverses it. |
| `fuingo account create EMAIL` | Adds an account and mails its invitation. Fails if the email already has an account, including one made by `game add --player`. |
| `fuingo account reset-password EMAIL` | Mails an active account a new link to choose a password: a reset, or another invitation if it has not chosen one yet. Its current password keeps working until the owner uses the link. |
| `fuingo account disable EMAIL` | Disables an account: it manages no games, plays in none, and cannot log in. `account enable` reverses it. |

Codes and emails are lowercased. A code is 1 to 32 characters: letters and
digits in any script, `-`, and `_`. An email is one or more printable ASCII
bytes with no spaces. Flags come before a command's argument, as in
`game create --system olympia g3`.

No command sets, prints, or reads a password. The commands that send mail
need `--public-url` and the [Mailgun settings](#mail), and check for them
before changing anything. If Mailgun then refuses the message, the account
(and player) stays, the command fails, and `account reset-password` sends
another link.

## Accounts and mail

Nobody can sign up. An account starts without a password, and its owner
chooses one through a link mailed to the account's address.

| Page | Does |
| --- | --- |
| `/` | The signed-in account's page, for now only its email and a sign-out button. Sends anyone else to `/sign-in`. |
| `/sign-in` | Signs an account in with its email and password. |
| `/sign-out` | Ends the browser's session. `POST` only. |
| `/account/password?token=...` | Where a mailed link leads: a form to choose the password. |
| `/account/reset` | Asks for a link by email address. Open to anyone. |

- A link works once and expires 72 hours after it was sent. Sending an
  account a new link cancels its earlier ones. Only a hash of a link's token
  is stored, and links are never logged.
- A password is at least 8 characters and at most 72 bytes, with no
  control characters.
- Choosing a password signs nobody in, and ends every session the account
  has, on the web and in `/api/`: sign in again with the new password.
- A web session is a random token in the cookie `__Host-fuingo_session`
  (`Secure`, `HttpOnly`, `SameSite=Lax`), stored only as a hash. It lasts 30
  days from its last use. Signing out ends it on the server. A disabled
  account's session is refused, and works again if the account is enabled
  before it expires.
- Every failed sign-in gets the same answer, whether the email is unknown,
  the password is wrong, or the account is disabled or has no password.
- `/account/reset` gives the same answer whether or not the address has an
  account, and mails nothing to an unknown or disabled one. An account that
  has not chosen a password is sent its invitation again, so this also
  replaces an expired invitation. Each client address may ask 5 times, then
  once more per minute, and gets `429` beyond that.
- Fuingo sends no other mail: nothing announces a file or a turn.

### Mail

Mail goes through [Mailgun](https://www.mailgun.com/). Its settings are
secrets: they come only from the environment, and have no flags.

| Environment variable | Meaning |
| --- | --- |
| `FUINGO_MAILGUN_DOMAIN` | The Mailgun sending domain. Required to send mail. |
| `FUINGO_MAILGUN_API_KEY` | The Mailgun API key. Required to send mail. |
| `FUINGO_MAILGUN_FROM` | The `From` address of every message. Required to send mail. |
| `FUINGO_MAILGUN_API_BASE` | The Mailgun API's origin. Default `https://api.mailgun.net`; use `https://api.eu.mailgun.net` for an EU domain. |
| `FUINGO_MAILGUN_SANDBOX_TO_ADDR` | For a Mailgun sandbox domain, which may send to one address only. Every message is delivered to this address instead, with the intended recipient at the start of its subject. Refused in `production`. |

- Set all three required settings or none; some but not all is an error.
- With none, `fuingod` starts in `development`, `test`, and `agent`, logs a
  warning, and sends nothing; the `fuingo` commands that send mail fail. In
  `production`, `fuingod` does not start without them.
- Fuingo does not handle mail that fails later. Bounces show in Mailgun.

## Configuration

Every flag can be set with an environment variable instead. Explicit flags
override environment variables, which override the defaults.

| Flag | Environment variable | Commands | Meaning |
| --- | --- | --- | --- |
| `--env` | `FUINGO_ENV` | all | `development`, `test`, `production`, or `agent`. Required, except by `fuingo version` without `--schema`. |
| `--db` | `FUINGO_DB` | all | Database file. Required. `fuingod` accepts `:memory:` in `development` and `agent` only; `fuingodb` and `fuingo` never do. `fuingo version` uses it only with `--schema`. |
| `--addr` | `FUINGO_ADDR` | `fuingod` | Backend listen address, `host:port`. Default `127.0.0.1:18443`. |
| `--trusted-proxies` | `FUINGO_TRUSTED_PROXIES` | `fuingod` | Comma-separated addresses and CIDR prefixes of the reverse proxies whose `X-Forwarded-For` header is trusted. Default `127.0.0.0/8,::1` (loopback); empty trusts none. |
| `--max-api-upload` | `FUINGO_MAX_API_UPLOAD` | `fuingod` | Largest file accepted through `/api/`, in bytes. Default `1048576` (1 MiB). Changing it never affects a file already stored. |
| `--schema` | (none) | `fuingo version` | Print the database's schema version. Command line only. |
| `--public-url` | `FUINGO_PUBLIC_URL` | `fuingod`, `fuingo` | The origin players reach Fuingo at, such as `https://fuingo.example.com`, which mailed links point to: `https`, with no path. Defaults to `https://htmx-app.localhost:8443` in `development` and `agent`; required elsewhere by `fuingod` and by the `fuingo` commands that send mail. |
| `--system` | (none) | `fuingo game` commands | The code of the game's system. Required. Command line only. |
| `--manager`, `--player`, `--code` | (none) | `fuingo game add`, `disable`, `enable` | The GM's or player's email, and the game's code. `--code` is required; `game add` needs exactly one of the others, and `disable` and `enable` at most one. Command line only. |

- The environment is read from `--env` or the process's `FUINGO_ENV`. The
  commands then load that environment's files from the working directory, in
  this order of precedence: the process environment, `.env.<env>.local`,
  `.env.local`, `.env.<env>`, `.env`.
- Flags marked "command line only" ignore their `FUINGO_` variables, so an
  environment file cannot change what a command does.
- The [Mailgun settings](#mail) are the only secrets. They have no flags;
  keep them in a `.env.<env>.local` file, which Git ignores, or in the
  process environment. `FUINGO_PEPPER`, which the previous design required,
  is no longer read; remove it from environment files.
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
| `GET`, `POST` `/api/systems/{system}/games/{game}/files` | Lists the files at the game's level, or uploads one there. |
| `GET`, `POST` `/api/systems/{system}/games/{game}/turns/{turn}/files` | The same, for a turn. |
| `GET`, `POST` `/api/systems/{system}/games/{game}/turns/{turn}/players/{id}/files` | The same, for a player's folder in a turn. |
| `GET /api/systems/{system}/games/{game}/uploads` | Lists the game's uploads at every level; `?after=ID` gives those after one already seen. |
| `GET /api/systems/{system}/games/{game}/uploads/{upload}` | One upload's record. |
| `GET /api/systems/{system}/games/{game}/uploads/{upload}/contents` | Downloads the file, exactly as uploaded. |
| `POST /api/systems/{system}/games/{game}/uploads/{upload}/withdraw` | Withdraws a file the account uploaded. |

Log in with an account whose owner has chosen a password through its
invitation. An account reaches only the games it manages.

## Proxy

`fuingod` serves plain HTTP and expects a TLS-terminating reverse proxy that
preserves `Host`, `Origin`, and `Sec-Fetch-Site`. In development, Caddy serves
`https://htmx-app.localhost:8443` and forwards to `127.0.0.1:18443`. See
[PROXIES.md](PROXIES.md).

### Client addresses and failed logins

Failed sign-ins are limited per client address, on the web and through
`/api/login` together: each address may fail 5 times, then once more per
minute, and gets `429 Too Many Requests` while over the
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

4. Write the settings that differ per server, including the secrets, to
   `/etc/fuingo/fuingo.env`, readable by root and the `fuingo` group only:

   ```sh
   sudo install -d -m 0755 /etc/fuingo
   sudo install -o root -g fuingo -m 0640 /dev/null /etc/fuingo/fuingo.env
   sudoedit /etc/fuingo/fuingo.env
   ```

   ```sh
   FUINGO_PUBLIC_URL=https://uploads.example.com
   FUINGO_MAILGUN_DOMAIN=mg.example.com
   FUINGO_MAILGUN_API_KEY=...
   FUINGO_MAILGUN_FROM=Fuingo <fuingo@mg.example.com>
   ```

   `fuingod` does not start in production without these.

5. Install and start the service:

   ```sh
   sudo cp contrib/systemd/fuingod.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now fuingod
   journalctl -u fuingod -f
   ```

6. Point Caddy at it. Caddy obtains the certificate for the domain itself:

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

A small wrapper keeps the commands short. It reads the same settings file
as the service, so the commands can send mail:

```sh
fu() { sudo -u fuingo sh -c 'cd /var/lib/fuingo && set -a && . /etc/fuingo/fuingo.env && set +a && exec fuingo --env production --db /var/lib/fuingo/fuingo.db "$@"' fuingo "$@"; }
```

Create the system and a game in it:

```sh
fu system create olympia
fu game create --system olympia g3
```

Create the GM's account, which mails the GM an invitation to choose a
password, and make it a GM of the game:

```sh
fu account create gm@example.com
fu game add --manager gm@example.com --system olympia --code g3
```

Add the players. Each command prints that player's number, and mails an
invitation to each email that had no account:

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
