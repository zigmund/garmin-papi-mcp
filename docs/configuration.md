# Configuration

A single YAML file, passed with `-config`. Unknown keys are rejected. `deploy/config.example.yaml` is a loadable example (checked by a test). Durations use Go syntax (`30s`, `1h`; there is no `d` unit); `0` or unset means the default and negative values are rejected unless noted.

## Top level

| Key | Default | Description |
| --- | --- | --- |
| `listen` | `127.0.0.1:8788` | Address the HTTP server binds to |
| `public_url` | required | Public https origin, no path/query/fragment. The MCP URL is `<public_url>/mcp` |
| `allow_insecure_http` | `false` | Allow plain http for non-loopback hosts in `public_url` and redirect URIs. It does not apply to `garmin.base_url`, which may use http only for loopback hosts |
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

The keys are read only at startup: restart the service after changing them (SIGHUP, `systemctl reload`, reloads only the TLS certificate). Adding a new first key starts sealing new tokens with it and invalidates nothing, because the older keys still decrypt. Tokens sealed under a key stop working only once that key is removed and the service restarted; removing every old key signs everyone out.

## `oauth`

| Key | Default | Description |
| --- | --- | --- |
| `oauth.access_token_ttl` | `1h` | Access token lifetime, at most `24h` (tokens cannot be revoked) |
| `oauth.refresh_token_ttl` | `720h` | Idle lifetime of a refresh token |
| `oauth.max_session_ttl` | `2160h` | Absolute lifetime of a login from the code exchange; refreshing never extends it |
| `oauth.code_ttl` | `5m` | Authorization code lifetime, at most `10m` |
| `oauth.session_choices` | none | "Stay connected for" choices on the form: up to 8 entries, each a whole number of hours of at least `1h`, or `unlimited` (no absolute end; `refresh_token_ttl` still applies, and the form says so: "No fixed end (ends after 30 days unused)" with the default). Replaces `max_session_ttl` for new logins |
| `oauth.session_default` | first choice | One of the choices, pre-selected. Requires `session_choices` |
| `oauth.allowed_redirect_uris` | claude.ai, claude.com and ChatGPT callbacks | Exact-match allow-list: `https://claude.ai/api/mcp/auth_callback`, `https://claude.com/api/mcp/auth_callback`, `https://chatgpt.com/connector_platform_oauth_redirect`. The origin (scheme, host and port) of each entry is also an accepted `Origin` on `/mcp`, next to the origin of `public_url`; any other `Origin` is refused with 403 (a request without `Origin` is accepted) |

### `oauth.page`

Optional, shown on the authorize page (English; plain text, HTML-escaped).

| Key | Default | Description |
| --- | --- | --- |
| `oauth.page.contact` | none | `mailto:` address or `https://` URL, shown as a link (at most 256 bytes) |
| `oauth.page.notice` | none | Operator notice, at most 4 KiB; paragraphs split on blank lines |
| `oauth.page.terms_file` | none | Plain-text terms (UTF-8, at most 32 KiB, read at startup, relative path resolved against the config directory). When set, the user must tick an acceptance box. Write your own; `deploy/terms.example.txt` is an example, not legal advice |

### `oauth.rate_limit`

Per-IP token buckets (sustained `per_minute`, bursts up to `burst`), a per-network budget of rejected keys, plus two unkeyed ceilings, answered with 429 and `Retry-After`. Defaults are generous because claude.ai and ChatGPT share egress addresses (the authorize form, though, is submitted from the user's own browser).

| Key | Default (per_minute / burst) | Applies to |
| --- | --- | --- |
| `oauth.rate_limit.register` | 60 / 30 | `POST /oauth/register` |
| `oauth.rate_limit.authorize` | 30 / 10 | `POST /oauth/authorize` |
| `oauth.rate_limit.token` | 300 / 100 | `POST /oauth/token` |
| `oauth.rate_limit.authorize_form` | 300 / 100 | `GET /oauth/authorize` |
| `oauth.rate_limit.authorize_global` | derived from `code_ttl` (2500 / 2500 at `5m`) | All `POST /oauth/authorize` together, unkeyed; counts submissions that are about to get a code |
| `oauth.rate_limit.key_check_global` | 300 / 100 | All Garmin key checks of `POST /oauth/authorize` together, unkeyed, whatever the outcome (accepted, rejected, no verdict). Each check is a Garmin call made for whoever submits the form; beyond the ceiling the form answers 429 with `Retry-After` and "try again" without calling Garmin |
| `oauth.rate_limit.key_rejected` | 5 / 10 | Keys Garmin rejects on `POST /oauth/authorize`, per network of the submitter: the /24 of an IPv4 address, the /48 of an IPv6 address. Charged before the check and given back unless Garmin rejects the key, so accepted keys (and checks without a verdict) cost nothing. A network out of budget gets 429 with `Retry-After` without a Garmin call and without spending `key_check_global`, so junk keys from a few networks cannot lock everyone else out of logging in: holding `key_check_global` down takes `key_check_global.per_minute / key_rejected.per_minute` networks (60 by default). The price is that everyone in such a network, honest users behind the same NAT included, waits for the refill even with a good key; raise it if many of your users share one network |
| `oauth.rate_limit.disabled` | `false` | Switches off all of the above, the ceilings and the per-network budget included |

## `limits` (MCP endpoint)

Zero or unset means the default; `-1` disables a single limit; `limits.disabled: true` turns all of them off (the body cap stays).

| Key | Default | Description |
| --- | --- | --- |
| `limits.ip.per_minute` / `burst` | 300 / 60 | Per client IP, charged before the token is opened and given back once the token authenticates and the body is read: it caps unauthenticated and invalid traffic only, because claude.ai and ChatGPT call for all their users from a few addresses. Authenticated calls are governed by the per-grant limits |
| `limits.token.per_minute` / `burst` | 60 / 20 | Per grant (one Personal API key, shared by all OAuth clients) |
| `limits.max_concurrent_per_token` | `16` | In-flight requests per grant. A request counts from before its body is read, so this also bounds the bodies a grant has being read at once |
| `limits.max_in_flight` | `1024` | In-flight requests overall, bodies being read included; beyond it 503 |
| `limits.max_request_body` | `65536` | Request body cap in bytes (413 beyond), from 1024 to 65536 (a larger value is rejected at startup, not clamped); cannot be disabled. A tool call is well under 4 KiB |
| `limits.disabled` | `false` | Turns off all limits above except the body cap |

Fixed, not configurable: on every path request headers must arrive within 10 s and the body within 30 s, also the body of a refused request (net/http reads it before answering); the OAuth endpoints allow 10 s for the whole request. These bound reading the request only, never a slow tool call. On SIGTERM or SIGINT the server stops accepting connections and gives in-flight requests 20 s (one Garmin request at the default `garmin.timeout`); tool calls still running are then cancelled and answered with a tool error asking to retry, and 5 s later the remaining connections are closed. The bundled systemd unit's `TimeoutStopSec=35s` covers those 25 s.

## `garmin`

| Key | Default | Description |
| --- | --- | --- |
| `garmin.base_url` | `https://personal.connectapi.garmin.com` | Garmin API origin: https, or http for a loopback host only; `allow_insecure_http` does not apply, because every user's API key is sent there |
| `garmin.timeout` | `20s` | Per Garmin request, at most `5m` |

## `tls`

Optional built-in TLS; leave unset behind a reverse proxy.

| Key | Default | Description |
| --- | --- | --- |
| `tls.cert_file`, `tls.key_file` | none | Set both or neither. Relative paths are resolved against the config directory |
| `tls.reload_interval` | `1m` | How often the files are re-checked (also on SIGHUP); a bad new pair keeps the old certificate |

### Let's Encrypt with built-in TLS

The unit runs the service as a transient user (`DynamicUser=yes`), and certbot keeps `/etc/letsencrypt/live` and `/etc/letsencrypt/archive` at `0700 root` and the private keys at `0600`, so out of the box the service gets `loading tls certificate: ... permission denied` at start. `live/<name>/*.pem` are symlinks into `archive/<name>/`, which holds numbered files (`privkey1.pem`, `privkey2.pem`, ...); every renewal writes a new set there and repoints the symlinks. Get the certificate first, for example `certbot certonly --standalone -d mcp.example.com` (needs port 80 free) or `--webroot`, then pick one of the two ways below (also sketched in `deploy/garmin-papi-mcp.service`); the examples use the certificate name `mcp.example.com`.

**a) Read in place, with hot reload.** A static group gets read access to this one certificate, and the dynamic user joins it via `SupplementaryGroups=` (a static group from `/etc/group` works with `DynamicUser=`).

1. Create the group: `groupadd --system tls-certs`.
2. Install a certbot deploy hook. Certbot runs every executable in `/etc/letsencrypt/renewal-hooks/deploy/` after each successful renewal, so the new key file gets the permissions too, then the service is told to load it:

   ```sh
   #!/bin/sh
   # /etc/letsencrypt/renewal-hooks/deploy/garmin-papi-mcp (chmod 0755)
   set -eu
   name=mcp.example.com
   le=/etc/letsencrypt
   # Only for this certificate (RENEWED_LINEAGE is unset when run by hand).
   case "${RENEWED_LINEAGE:-$le/live/$name}" in "$le/live/$name") ;; *) exit 0 ;; esac
   # Directories: the group needs x to traverse them to the files.
   chgrp tls-certs "$le/live" "$le/archive" "$le/live/$name" "$le/archive/$name"
   chmod g+rx "$le/live" "$le/archive" "$le/live/$name" "$le/archive/$name"
   # Keys: readable by the group, never by others.
   chgrp tls-certs "$le/archive/$name"/privkey*.pem
   chmod 0640 "$le/archive/$name"/privkey*.pem
   # Load the new pair now instead of within tls.reload_interval
   # (SIGHUP via ExecReload; does nothing while the service is stopped).
   systemctl try-reload-or-restart garmin-papi-mcp
   ```

   Other certificates on the host stay unreadable: the group can list `live/` and `archive/`, but their keys keep `0600 root`.
3. Run the hook once by hand to fix the current files: `/etc/letsencrypt/renewal-hooks/deploy/garmin-papi-mcp`.
4. Add the group to the unit with a drop-in, `systemctl edit garmin-papi-mcp`:

   ```ini
   [Service]
   SupplementaryGroups=tls-certs
   ```

5. Point the config at the `live/` symlinks, which are re-read on every check, so a renewal is picked up without a restart:

   ```yaml
   tls:
     cert_file: /etc/letsencrypt/live/mcp.example.com/fullchain.pem
     key_file: /etc/letsencrypt/live/mcp.example.com/privkey.pem
   ```

6. `systemctl restart garmin-papi-mcp` (group membership is set only at start); the log shows `tls certificate loaded`. To test the access alone: `systemd-run --wait --quiet -p DynamicUser=yes -p SupplementaryGroups=tls-certs test -r /etc/letsencrypt/live/mcp.example.com/privkey.pem && echo readable`.

**b) Private copy, restart on renewal.** `LoadCredential=` makes systemd (as root) copy the files into the service's credentials directory at start, so no permissions change. The copy is not updated on renewal, so hot reload has nothing to pick up and the service needs a restart. Add to the unit with `systemctl edit garmin-papi-mcp` (this adds to the unit's `LoadCredential=keys:...`, it does not replace it):

```ini
[Service]
LoadCredential=fullchain.pem:/etc/letsencrypt/live/mcp.example.com/fullchain.pem
LoadCredential=privkey.pem:/etc/letsencrypt/live/mcp.example.com/privkey.pem
```

```yaml
tls:
  cert_file: /run/credentials/garmin-papi-mcp.service/fullchain.pem
  key_file: /run/credentials/garmin-papi-mcp.service/privkey.pem
```

and restart after each renewal from a deploy hook:

```sh
#!/bin/sh
# /etc/letsencrypt/renewal-hooks/deploy/garmin-papi-mcp (chmod 0755)
[ "${RENEWED_LINEAGE:-}" = /etc/letsencrypt/live/mcp.example.com ] || exit 0
exec systemctl try-restart garmin-papi-mcp
```

The restart drops open connections for a moment; issued tokens stay valid, since they are sealed rather than stored.

## Reverse proxy

By default the service speaks plain HTTP on `listen` (`127.0.0.1:8788`), so put a TLS-terminating reverse proxy in front of it (or enable built-in TLS, see `tls`, and expose it directly). The proxy must forward every path (`/.well-known/*`, `/oauth/*`, `/mcp`) to the service. Below is an nginx vhost for `garmin-papi-mcp.zigmund.xyz`; adapt the name to your host.

Get the certificate with certbot first, for example `certbot certonly --nginx -d garmin-papi-mcp.zigmund.xyz` (or `--webroot`); its renewal timer keeps it fresh, and nginx picks up a renewed certificate on `systemctl reload nginx` (certbot's nginx plugin does that for you).

```nginx
# /etc/nginx/conf.d/garmin-papi-mcp.conf

server {
    listen 80;
    listen [::]:80;
    server_name garmin-papi-mcp.zigmund.xyz;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;                           # nginx >= 1.25.1; on older nginx use `listen 443 ssl http2;`
    server_name garmin-papi-mcp.zigmund.xyz;

    ssl_certificate     /etc/letsencrypt/live/garmin-papi-mcp.zigmund.xyz/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/garmin-papi-mcp.zigmund.xyz/privkey.pem;

    # HSTS: the service sends it itself only with built-in TLS.
    add_header Strict-Transport-Security "max-age=31536000" always;

    # The service accepts at most limits.max_request_body (64 KiB by default)
    # per MCP request (413 beyond); the authorize form is far smaller.
    client_max_body_size 1m;

    proxy_http_version 1.1;
    proxy_set_header Connection "";     # keepalive to the service
    proxy_set_header Host       $host;
    # Overwrite, never append ($proxy_add_x_forwarded_for): the service trusts
    # these headers from a loopback peer, so a header sent by the client must
    # not reach it, or per-IP limits can be dodged.
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Real-IP       $remote_addr;
    # JSON responses only, but do not hold requests or answers in nginx.
    proxy_buffering         off;
    proxy_request_buffering off;
    # A tool call makes one or two Garmin requests of up to garmin.timeout each.
    proxy_read_timeout      120s;

    # Optional: /healthz is for local checks.
    location = /healthz { return 404; }

    location / {
        proxy_pass http://127.0.0.1:8788;
    }
}
```

**`trusted_proxies` and client IPs.** The service takes the client IP for its rate limits and logs from `X-Forwarded-For` / `X-Real-IP` only when the TCP peer is in `trusted_proxies` (loopback by default, which is nginx on the same host). With a bare `proxy_pass` nginx forwards whatever `X-Forwarded-For` / `X-Real-IP` the client sent, and the service would believe it: every request could claim a fresh address and get around the per-IP limits. Always overwrite both headers as above. Never add the addresses of untrusted hosts to `trusted_proxies`; behind a second tier (a CDN or load balancer) add only that tier's addresses, and keep loopback in the list if nginx still connects from it (setting `trusted_proxies` replaces the default).

The service rate-limits the OAuth endpoints and `/mcp` itself (see `oauth.rate_limit` and `limits`), so nginx needs no `limit_req`. If you add one, keep it generous: claude.ai and ChatGPT call from a few shared egress addresses for all their users.

## When a key stops working

A Personal API key expires on the date its owner chose, or is revoked at Garmin. The server learns of it only when Garmin refuses the key-to-JWT exchange (`401`). It exchanges the key again only when the Garmin session (JWT, valid up to 24 hours) it cached for the key is about to expire, after a restart, or when Garmin refuses the cached JWT; until then tool calls can keep working.

Once Garmin refuses the key on a tool call:

- that call gets HTTP `401` with a `WWW-Authenticate: Bearer` challenge carrying `error="invalid_token"`, `error_description="Garmin rejected the API key"` and the `resource_metadata` URL;
- the key is remembered as rejected (by a MAC of it, in memory, for `oauth.access_token_ttl`): further calls with any token carrying it get the same `401` without another Garmin call, and the refresh grant answers `invalid_grant`, so the client cannot revive the dead key;
- the client (claude.ai, ChatGPT) asks the user to reconnect and opens the login form. Submitting the expired or revoked key there shows "Garmin rejected this key. Check it and try again."; submitting a new, working key signs the user in again (a key that Garmin accepts on the form is also cleared from the rejected set).

A data endpoint that answers `401` while the key itself still works (the exchange succeeds) does not count as a rejected key: only that tool call fails, with the tool error "Garmin returned HTTP 401".
