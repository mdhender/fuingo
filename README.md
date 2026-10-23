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
```

`fuingodb` creates and migrates databases. `fuingod` serves the upload form and
only opens an existing, current database; it never creates or migrates one.

## Configuration

Every flag can be set with an environment variable instead. Explicit flags
override environment variables, which override the defaults.

| Flag | Environment variable | Commands | Meaning |
| --- | --- | --- | --- |
| `--env` | `FUINGO_ENV` | both | `development`, `test`, `production`, or `agent`. Required. |
| `--db` | `FUINGO_DB` | both | Database file. Required. `fuingod` accepts `:memory:` in `development` and `agent` only; `fuingodb` never does. |
| `--addr` | `FUINGO_ADDR` | `fuingod` | Backend listen address, `host:port`. Default `127.0.0.1:18443`. |
| `--trusted-proxies` | `FUINGO_TRUSTED_PROXIES` | `fuingod` | Comma-separated addresses and CIDR prefixes of the reverse proxies whose `X-Forwarded-For` header is trusted. Default `127.0.0.0/8,::1` (loopback); empty trusts none. |
| (none) | `FUINGO_PEPPER` | `fuingod`, `fuingodb create` | Key for the upload credential hashes. Required. |

- The environment is read from `--env` or the process's `FUINGO_ENV`. The
  commands then load that environment's files from the working directory, in
  this order of precedence: the process environment, `.env.<env>.local`,
  `.env.local`, `.env.<env>`, `.env`.
- `FUINGO_PEPPER` is a secret and has no flag. Keep it in the process
  environment or an ignored `.local` file, never in `.env` or `.env.<env>`.
  Changing it invalidates every stored upload token.
- `fuingodb create` in `development` and `agent` seeds the default games and
  upload accounts listed in [TESTING.md](TESTING.md). So does
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
