# Configuration

A single YAML file, passed with `-config`. Unknown keys are rejected. `deploy/config.example.yaml` is a loadable example (checked by a test). Durations use Go syntax (`30s`, `1h`; there is no `d` unit); `0` or unset means the default and negative values are rejected unless noted.

## Top level

| Key | Default | Description |
| --- | --- | --- |
| `listen` | `127.0.0.1:8787` | Address the HTTP server binds to |
| `public_url` | required | Public https origin, no path/query/fragment. The MCP URL is `<public_url>/mcp` |
| `allow_insecure_http` | `false` | Allow plain http for non-loopback hosts in `public_url`, redirect URIs and `garmin.base_url` |
| `trusted_proxies` | `127.0.0.0/8`, `::1/128` | CIDRs or IPs whose `X-Forwarded-For` / `X-Real-IP` are trusted for the client IP. Setting it replaces the default; `/0` prefixes are rejected |

## `log`

| Key | Default | Description |
| --- | --- | --- |
| `log.level` | `info` | `debug` (enables the access log), `info`, `warn`, `error` |
| `log.format` | `json` | `json` or `text` |

## `secrets`

Set exactly one of the two keys.

| Key | Default | Description |
| --- | --- | --- |
| `secrets.token_keys_file` | none | File with one standard-base64 32-byte key per line (`#` comments and blank lines ignored). The first key encrypts, the rest only decrypt. A relative path is resolved against the config directory. With the bundled unit: `/run/credentials/garmin-papi-mcp.service/keys` |
| `secrets.token_keys` | none | The same keys inline. Avoid: the config is usually world-readable |

Generate a key with `openssl rand -base64 32`. Known-weak keys (all-zero, repeated-byte, incrementing patterns) and duplicates are rejected.

## `oauth`

| Key | Default | Description |
| --- | --- | --- |
| `oauth.access_token_ttl` | `1h` | Access token lifetime, at most `24h` (tokens cannot be revoked) |
| `oauth.refresh_token_ttl` | `720h` | Idle lifetime of a refresh token |
| `oauth.max_session_ttl` | `2160h` | Absolute lifetime of a login from the code exchange; refreshing never extends it |
| `oauth.code_ttl` | `5m` | Authorization code lifetime, at most `10m` |
| `oauth.session_choices` | none | "Stay connected for" choices on the form: up to 8 entries, each a whole number of hours of at least `1h`, or `unlimited` (no absolute end; `refresh_token_ttl` still applies). Replaces `max_session_ttl` for new logins |
| `oauth.session_default` | first choice | One of the choices, pre-selected. Requires `session_choices` |
| `oauth.allowed_redirect_uris` | claude.ai, claude.com and ChatGPT callbacks | Exact-match allow-list: `https://claude.ai/api/mcp/auth_callback`, `https://claude.com/api/mcp/auth_callback`, `https://chatgpt.com/connector_platform_oauth_redirect` |

### `oauth.page`

Optional, shown on the authorize page (English; plain text, HTML-escaped).

| Key | Default | Description |
| --- | --- | --- |
| `oauth.page.contact` | none | `mailto:` address or `https://` URL, shown as a link (at most 256 bytes) |
| `oauth.page.notice` | none | Operator notice, at most 4 KiB; paragraphs split on blank lines |
| `oauth.page.terms_file` | none | Plain-text terms (UTF-8, at most 32 KiB, read at startup, relative path resolved against the config directory). When set, the user must tick an acceptance box. Write your own; `deploy/terms.example.txt` is an example, not legal advice |

### `oauth.rate_limit`

Per-IP token buckets (sustained `per_minute`, bursts up to `burst`) answered with 429 and `Retry-After`. Defaults are generous because claude.ai and ChatGPT share egress addresses.

| Key | Default (per_minute / burst) | Applies to |
| --- | --- | --- |
| `oauth.rate_limit.register` | 60 / 30 | `POST /oauth/register` |
| `oauth.rate_limit.authorize` | 30 / 10 | `POST /oauth/authorize` |
| `oauth.rate_limit.token` | 300 / 100 | `POST /oauth/token` |
| `oauth.rate_limit.authorize_form` | 300 / 100 | `GET /oauth/authorize` |
| `oauth.rate_limit.authorize_global` | derived from `code_ttl` (2500 / 2500 at `5m`) | All `POST /oauth/authorize` together, unkeyed |
| `oauth.rate_limit.disabled` | `false` | Switches off all of the above |

## `limits` (MCP endpoint)

Zero or unset means the default; `-1` disables a single limit; `limits.disabled: true` turns all of them off (the body cap stays).

| Key | Default | Description |
| --- | --- | --- |
| `limits.ip.per_minute` / `burst` | 300 / 60 | Per client IP, before the token is opened |
| `limits.token.per_minute` / `burst` | 60 / 20 | Per grant (one Personal API key, shared by all OAuth clients) |
| `limits.max_concurrent_per_token` | `8` | In-flight requests per grant |
| `limits.max_in_flight` | `1024` | In-flight requests overall; beyond it 503 |
| `limits.max_request_body` | `10485760` | Request body cap in bytes (413 beyond), at least 1024; cannot be disabled |
| `limits.disabled` | `false` | Turns off all limits above except the body cap |

## `garmin`

| Key | Default | Description |
| --- | --- | --- |
| `garmin.base_url` | `https://personal.connectapi.garmin.com` | Garmin API origin (https unless loopback or `allow_insecure_http`) |
| `garmin.timeout` | `20s` | Per Garmin request, at most `5m` |

## `tls`

Optional built-in TLS; leave unset behind a reverse proxy.

| Key | Default | Description |
| --- | --- | --- |
| `tls.cert_file`, `tls.key_file` | none | Set both or neither. Relative paths are resolved against the config directory |
| `tls.reload_interval` | `1m` | How often the files are re-checked (also on SIGHUP); a bad new pair keeps the old certificate |
