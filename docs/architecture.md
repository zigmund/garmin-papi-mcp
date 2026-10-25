# Architecture

garmin-papi-mcp is one Go binary that is both an OAuth 2.1 authorization server and a stateless MCP server. The "login" of the authorization server is a form asking for the user's Garmin Personal API key. The key is sealed into the OAuth tokens, so there is no database. A single instance is assumed.

## Packages

| Package | Role |
| --- | --- |
| `cmd/garmin-papi-mcp` | Entry point: flags, config load, wiring, graceful shutdown |
| `internal/config` | Strict YAML config, defaults, validation, token key loading |
| `internal/seal` | AES-256-GCM sealing of token payloads (kind-bound, key rotation: first key encrypts, all decrypt) |
| `internal/tokens` | Payload types sealed into access and refresh tokens and authorization codes |
| `internal/oauth` | Metadata, dynamic client registration, authorize form, token endpoint, PKCE, replay caches, OAuth rate limits |
| `internal/garmin` | Garmin Personal API client: key-to-JWT exchange, in-memory JWT cache (single flight), data requests, error mapping |
| `internal/tools` | The 17 MCP tools: parameters, date validation, endpoint mapping, downsampling, result shaping |
| `internal/mcp` | Stateless MCP Streamable HTTP endpoint (JSON only): bearer check, JSON-RPC, tool dispatch |
| `internal/ratelimit` | Token-bucket limiter per key, concurrency limiter |
| `internal/httplog` | Access log and client IP resolution (trusted proxies) |
| `internal/logging` | slog setup |
| `internal/certreload` | Optional built-in TLS with certificate hot reload |

## Request routing

| Path | Handler | Notes |
| --- | --- | --- |
| `GET /healthz` | health check | no auth, no Garmin call |
| `GET /.well-known/oauth-authorization-server`, `/.well-known/openid-configuration` | authorization server metadata (RFC 8414) | static, cacheable |
| `GET /.well-known/oauth-protected-resource[/mcp]` | protected resource metadata (RFC 9728) | points at this server as its own authorization server |
| `POST /oauth/register` | dynamic client registration | per-IP rate limit |
| `GET /oauth/authorize` | credentials form | per-IP rate limit; shows redirect host, client name (unverified), operator notice and terms |
| `POST /oauth/authorize` | validates the key against Garmin, issues a code, redirects | per-IP rate limit; global ceilings on Garmin key checks (any outcome) and on issued codes |
| `POST /oauth/token` | code and refresh token exchange | per-IP rate limit; refresh tokens are single use |
| `POST /mcp` | MCP JSON-RPC (`initialize`, `tools/list`, `tools/call`) | bearer token required; per-IP and per-grant limits, concurrency caps, body cap |

A missing, invalid or expired bearer token, or a Personal API key that Garmin rejects (its key-to-JWT exchange answers 401), gives `401` with a `WWW-Authenticate` challenge so the client re-runs OAuth. A rejected key is remembered (by a MAC, in memory) until the access token lifetime has passed or the user signs in again with a working key: until then its tokens get `401` without another Garmin call and the refresh grant answers `invalid_grant`.

`POST /mcp` also checks the `Origin` header (DNS-rebinding protection for browsers): a request without one is accepted; a request with exactly one is accepted only when it is a bare origin equal to the origin of `public_url` or to the origin (scheme, host and port) of an entry of `oauth.allowed_redirect_uris`, because the backends of the platforms this server redirects to (claude.ai, claude.com, ChatGPT by default) may send their own `Origin`. Anything else (another origin, `null`, userinfo, a path, query or fragment, several `Origin` lines) gets `403`. Authentication is by bearer token only, never cookies.

## Tool call

1. `Origin` check, then the per-IP limit, then the bearer token is opened (AES-256-GCM); it yields the Personal API key. A missing, invalid or rejected token is refused here, with the per-IP charge kept.
2. Body cap and encoding check, then the body is read (under a deadline). Once it is in, the per-IP charge is given back: authenticated traffic is governed by the per-grant limits only.
3. Per-grant limits (a grant is one key, shared by all OAuth clients): rate, concurrency, then the global in-flight cap.
4. The tool validates its arguments (date format, range length) and calls one or two Garmin endpoints.
5. The Garmin client uses the cached JWT for the key (hash of the key), or exchanges the key for a new one (24 h lifetime, refreshed shortly before expiry). On a Garmin 401 it drops the JWT, exchanges once more and retries once. A 401 from that exchange means the key is rejected (see above); a data endpoint that still answers 401 with the fresh JWT (for example one the key has no access to) only fails that tool call, with the tool error "Garmin returned HTTP 401", and the key stays usable for the other tools.
6. The result is shaped for the model: large series downsampled with min/max/mean kept, empty results as "no data", partial failures reported next to the part that succeeded.

## State

In memory only, lost on restart: authorization code and refresh chain replay caches, rate limiters, the Garmin JWT cache. Nothing is written to disk.
