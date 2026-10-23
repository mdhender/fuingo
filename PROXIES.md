# Reverse proxy and listener guidance

Read this before implementing HTTP startup, network configuration, redirects,
cookies, or CSRF protection. These are application requirements, not a claim that
an HTTP server has already been implemented.

For development/agent-only POST account and login helpers, also follow
[TESTING.md](TESTING.md). Keep their environment gating and the CSRF
requirements below intact.

## Caddy is externally managed

- The user runs Caddy in development with configuration at
  `/opt/homebrew/etc/Caddyfile` on their machine.
- **Do not edit that file or change Caddy configuration.** Do not reconfigure,
  reload, or replace Caddy as part of application development.
- Test and production deployments also run behind a Caddy reverse proxy.
  Caddy guarantees TLS 1.3 termination for browser-facing connections on the
  user's laptop and in test and production. Agent orbs use Amp's portal proxy
  instead; do not apply the Caddy-specific configuration or TLS guarantee there.
- The application is the backend, not the TLS terminator. Serve HTTP on the
  configured backend listener; do not add application-side certificates or
  require backend TLS merely because the public URL uses HTTPS.

## Laptop development addresses

| Purpose | Address |
| --- | --- |
| Browser-facing HTTPS origin | `https://htmx-app.localhost:8443` |
| Application listener port | `18443` |
| Preferred local backend bind address | `127.0.0.1:18443` |

Caddy proxies the browser-facing origin to the application. Keep the public
origin and backend listen address distinct: redirects, absolute links, and cookie
policy must reflect the public HTTPS origin, not the backend port. A missing
proxy connection is not permission to change Caddy configuration.

## Configurable listener

- Accept the network listen address (`host:port`) through command-line parameters
  or environment variables. Do not hard-code port `18443` as the only option;
  test and production may require different ports or bind addresses.
- Use `internal/dotenv` when loading environment files. Supported environments
  are `development`, `test`, `production`, and `agent`; `agent` is reserved for
  the coding agent's local work.
- Preserve the loader's precedence: the existing process environment wins,
  followed by `.env.<environment>.local`, `.env.local`, `.env.<environment>`,
  then `.env`. Even an explicitly empty process variable is not overwritten.
- If both flags and environment configuration are offered, explicitly supplied
  flags take precedence over environment values, with defaults last. Validate
  the resolved address and report bind failures clearly.
- Do not invent existing flag or environment-variable names: define and document
  them when implementing server startup. Shared environment files must never
  contain secrets; keep secret/local overrides in the ignored `.local` files.

## Proxy boundary and CSRF

- Wrap browser-facing handlers with the Go standard library's
  `http.NewCrossOriginProtection().Handler(...)`. This applies to ordinary forms
  and HTMX requests, including login and registration; HTMX is not a CSRF bypass.
- Caddy's browser-facing HTTPS permits secure browser behavior, including Fetch
  Metadata headers. TLS 1.3 alone does not prevent CSRF: the middleware must
  actually run and check `Sec-Fetch-Site`, or fall back to comparing `Origin`
  against the request's `Host` (including the port).
- The proxy integration must preserve the public `Host`, `Origin`, and
  `Sec-Fetch-Site` semantics. Do not rewrite `Host` to the backend address or strip
  these headers in application middleware. Verify this against the existing
  proxy; report incompatibilities rather than editing Caddy configuration.
- Do not add trusted origins or insecure bypass patterns merely to make the
  proxy work. Normal same-origin requests need no exception. Do not use
  `HX-Request` or arbitrary forwarded headers as proof that a request is trusted.
- `GET`, `HEAD`, and `OPTIONS` are always allowed by this middleware and must not
  change application state. Requests without both Fetch Metadata and Origin
  headers are also allowed; CSRF protection does not replace authentication or
  authorization.
- TLS ends at Caddy (or Amp's portal proxy in agent orbs), so backend `r.TLS`
  may be nil for a public HTTPS request.
  Keep session cookies `Secure` and `HttpOnly` and use an appropriate `SameSite`
  policy. Do not downgrade cookie security based on the backend HTTP transport.
- Keep the backend reachable only through the intended trusted proxy boundary
  (loopback in local development, or deployment network controls). Trust forwarded
  metadata only from configured trusted proxies, never arbitrary clients. Do not
  derive public redirect origins from unvalidated client-supplied headers.

## Verification when implementing the server

- Verify the development listener uses port `18443` and can be overridden for
  test and production without modifying Caddy.
- Exercise browser requests through the existing public HTTPS origin, not just
  direct backend requests. Verify that normal form and HTMX submissions succeed,
  cross-origin unsafe requests are rejected, and redirects and cookies retain
  the public HTTPS origin and security settings.
- Cover the middleware's Origin fallback with the public host and port, as well
  as rejection of mismatched origins. Keep these checks independent of any live
  Caddy installation where possible using Go HTTP tests.
- Use the orb's portal mechanism for agent-local browser checks. Report which
  proxy path was exercised, and explicitly state when the real Caddy path and
  its TLS 1.3 configuration have not been verified.
