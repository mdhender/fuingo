# fuingo

A web server that accepts turn-file uploads for play-by-email games. Players
upload a turn file through a web form instead of mailing it. The file's first
line, `<game code> <turn number> <email address> <token>`, authorizes it; the
server stores the rest of the file.

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
```

`fuingodb` creates and migrates databases. `fuingod` serves the upload form and
only opens an existing, current database; it never creates or migrates one.
`fuingo` is the operators' CLI for the application data. `fuingo version`
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
