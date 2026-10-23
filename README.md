# fuingo

A web server that accepts turn-file uploads for play-by-email games. Players
upload a file through a web form, authorized by their email address and upload
token, instead of mailing it.

## Commands

```sh
go run ./cmd/fuingodb --env development create --db tmp/fuingo.db
go run ./cmd/fuingodb --env development migrate --db tmp/fuingo.db
go run ./cmd/fuingod  --env development --db tmp/fuingo.db --upload-dir tmp/uploads
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
| `--upload-dir` | `FUINGO_UPLOAD_DIR` | `fuingod` | Directory for uploaded files. Required; it must already exist. |
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
- Uploaded files are named by the SHA-256 of their contents. Each successful
  upload is recorded in the `uploads` table.

## Proxy

`fuingod` serves plain HTTP and expects a TLS-terminating reverse proxy that
preserves `Host`, `Origin`, and `Sec-Fetch-Site`. In development, Caddy serves
`https://htmx-app.localhost:8443` and forwards to `127.0.0.1:18443`. See
[PROXIES.md](PROXIES.md).
