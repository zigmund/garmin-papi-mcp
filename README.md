# garmin-papi-mcp

A read-only [MCP](https://modelcontextprotocol.io) server for the Garmin Connect Personal API, with an OAuth 2.1 login form. It lets claude.ai, ChatGPT and other MCP clients read **your own** Garmin Connect data (sleep, heart rate, HRV, activities, weight, and so on) without custom headers: you paste your Personal API key into a form once, and the server turns it into OAuth tokens for the client.

A public instance is hosted by its author at `https://garmin-papi-mcp.zigmund.xyz` (MCP URL `https://garmin-papi-mcp.zigmund.xyz/mcp`). You can also run your own.

> **Read this first.** This project is **not affiliated with, endorsed by or supported by Garmin**. Garmin's Personal API key program is intended for individuals accessing their own data. Giving your key to a third-party server (this one, or any other instance) is your decision and your responsibility: check Garmin's terms before you do. The server is strictly read-only; it cannot change anything in your Garmin account. If you are not comfortable trusting an operator with your key, run your own instance.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/flow-dark.svg">
  <img alt="Flow: you paste your Personal API key on the server's own page; Garmin checks it and the server seals it into a token your AI client holds; on each tool call the server unseals it and reads your data from Garmin." src="docs/images/flow-light.svg">
</picture>

## How it works

```mermaid
sequenceDiagram
    participant U as You (browser)
    participant C as claude.ai / ChatGPT
    participant S as garmin-papi-mcp
    participant G as Garmin Connect API
    C->>S: discover OAuth metadata, register client
    C->>U: send you to /oauth/authorize
    U->>S: paste your Personal API key in the form
    S->>G: exchange the key for a token (validates the key)
    G-->>S: JWT (valid 24 h)
    S-->>C: access and refresh tokens (the key, sealed with AES-256-GCM)
    C->>S: MCP tool call with the bearer token
    S->>S: open the token, get or reuse the cached JWT
    S->>G: GET the data
    G-->>S: JSON
    S-->>C: tool result (trimmed for the model)
```

There is no database. The key lives only inside the sealed tokens; the client stores them and cannot decrypt them. Server-side state is in memory and lost on restart: single-use replay caches, rate limiters and a cache of Garmin JWTs keyed by a hash of the key. Losing the replay caches has a security cost, described under [Privacy and security](#privacy-and-security).

### Who sees what

| | Your Personal API key | Your Garmin data | Your OAuth tokens |
| --- | --- | --- | --- |
| **You** | yes (you create it) | yes | no |
| **claude.ai / ChatGPT** | no (they get a sealed token, which they cannot read) | yes, the tool results you ask for | yes, stored and sent with each call |
| **This server** | yes, in memory while handling a request (typed into the form, or opened from a token); never stored, never logged | yes, in transit only; never stored or logged | yes, it issues and opens them |
| **Garmin** | yes (used to authenticate) | yes (it is the source) | no |

A TLS-terminating reverse proxy in front of the server also sees the key in the form submission (and bearer tokens on every call). If you run your own instance, you are that operator.

## Tools

All 17 tools are read-only. Dates are `YYYY-MM-DD` in your own Garmin time zone. Large series (heart rate, stress, Body Battery, HRV, SpO2) are downsampled by default to keep answers small.

| Tool | What it returns |
| --- | --- |
| `get_profile` | Profile settings (units, time zone, preferences) and heart rate zones |
| `get_daily_summary` | Daily summary (steps, calories, distance, and more) for a date or a range |
| `get_sleep` | Sleep data for a date or a range |
| `get_heart_rate` | Heart rate for one date |
| `get_hrv` | Heart rate variability for a date or a range |
| `get_body_battery` | Body Battery for a date range |
| `get_stress` | Stress levels for a date range |
| `get_steps` | Daily steps for a date range |
| `get_spo2` | Pulse oximetry (SpO2) for one date |
| `list_activities` | Recorded activities with filters, trimmed summaries |
| `get_activity` | Full details of one activity and its workouts |
| `get_weight` | Weight and body composition entries plus the weight goal for a range |
| `get_nutrition` | Food log for one date |
| `get_hydration` | Hydration for a date range |
| `get_fitness_age` | Fitness age and its factors for one date |
| `get_wellness_activities` | Wellness activity summaries for a date range |
| `get_menstrual_cycle` | Menstrual cycle summary and day view for one date (many accounts have none) |

An empty result is reported as "no data for this period", not as an error. A Garmin rate limit is reported as a distinct tool error.

## Get a Personal API key

1. Sign in to Garmin Connect on the website.
2. Open the Personal API Key page described at <https://developer.garmin.com/gc-developer-program/personal/personal-api> and create a key. You choose its expiry.
3. Keep it secret: it grants read access to your data. Revoke it there at any time.

## Connect from claude.ai or ChatGPT

1. Add a custom connector (claude.ai: Settings -> Connectors -> Add custom connector; ChatGPT: Plugins -> Add -> Add custom MCP server; no Developer mode needed, and it works on the free plan too). In a ChatGPT workspace, the workspace settings may still restrict custom servers.
2. Enter the MCP URL: `<public_url>/mcp`, for example `https://garmin-papi-mcp.zigmund.xyz/mcp`.
3. When the client sends you to the login page, read it, paste your Personal API key, choose how long to stay connected, accept the terms if the operator set any, and press Authorize.

If Garmin rejects the key (expired or revoked), the form shows an error. When a key stops working later, the server answers 401 and the client asks you to log in again. That is not immediate: after a key expires or is revoked at Garmin, calls can keep working until the Garmin session the server cached for it expires (up to 24 hours).

### Make the assistant use it

Clients do not always call a connector on their own. To make it the default for health and training questions, send this once in a chat where the connector is enabled (or put it in your profile instructions):

```text
Remember this in your global memory:

I have a Garmin MCP connector (Garmin Connect Personal API, read-only). When I ask
about my health, sleep, training or how I feel, get the data from this connector
first; do not answer from general knowledge and do not ask me to send the data.

Use it for:
- sleep, heart rate and resting heart rate, HRV, Body Battery, stress, steps, SpO2,
  the daily summary;
- workouts and activities (running, cycling, swimming and so on), training load,
  recovery, fitness age;
- weight and body composition, nutrition, hydration, menstrual cycle;
- questions like "how did I sleep", "am I ready to train", "why am I tired",
  "how was my week", "compare with last month".

Rules:
- Default period: today or the last 7 days. If I name a period, use it.
- If there is no data for a day, say so; never make up numbers.
- If the connector is unavailable or the key is rejected, say so plainly and suggest
  reconnecting the connector.
- Lead with the numbers: key values first, then the conclusion.
```

On claude.ai memory works only with Settings -> Capabilities -> Memory turned on; the text is more reliable in Settings -> Profile ("What personal preferences should Claude consider"), which applies to every chat. On ChatGPT, check that the entry appeared under Settings -> Personalization -> Memory.

## Run your own

Requirements: a Linux host with systemd, a DNS name, a TLS-terminating reverse proxy (for example nginx) in front of `127.0.0.1:8788`, or the built-in TLS (`tls.*`). For nginx, use the vhost in [docs/configuration.md, Reverse proxy](docs/configuration.md#reverse-proxy): it must overwrite `X-Forwarded-For` and `X-Real-IP`, or clients can get around the per-IP limits.

```sh
# 1. Install the binary: `make build` writes bin/garmin-papi-mcp. From a release
#    archive, use the garmin-papi-mcp in its unpacked directory instead.
install -m 0755 bin/garmin-papi-mcp /usr/local/bin/garmin-papi-mcp

# 2. Config and token encryption key (root-only; the unit hands a private copy to the service).
install -d -m 0755 /etc/garmin-papi-mcp
install -m 0644 deploy/config.example.yaml /etc/garmin-papi-mcp/config.yaml   # edit public_url first
install -m 0600 -o root -g root /dev/null /etc/garmin-papi-mcp/keys
openssl rand -base64 32 > /etc/garmin-papi-mcp/keys

# 3. Install and start the unit.
install -m 0644 deploy/garmin-papi-mcp.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now garmin-papi-mcp
curl -fsS http://127.0.0.1:8788/healthz
```

Every config key is described in [docs/configuration.md](docs/configuration.md); the design is in [docs/architecture.md](docs/architecture.md). Because the unit uses `DynamicUser=` and `LoadCredential=`, the config file holds only the path `/run/credentials/garmin-papi-mcp.service/keys`, never key material. The token keys (and the rest of the config) are read only at startup, so after changing the keys file run `systemctl restart garmin-papi-mcp`; `systemctl reload` (SIGHUP) only reloads the TLS certificate. To start sealing with a new key, add it as the first line and restart: the older keys still decrypt, so this signs nobody out and cuts no access. Tokens sealed under a key stop working only once that key is removed from the file and the service restarted; removing every old key signs everyone out.

Optional: write your own terms of use (see `deploy/terms.example.txt`, which is an example and not legal advice) and set `oauth.page.terms_file`, `oauth.page.contact` and `oauth.page.notice`.

## Privacy and security

- **The key is sealed into the tokens.** It is AES-256-GCM encrypted with the operator's token key and carried in the access and refresh tokens. It is never stored on the server and never logged. Logs carry metadata only (tool name, status, duration, client IP); never tokens, keys, Garmin JWTs, request or response bodies, or query strings.
- **Tokens are bearer secrets and are not revoked.** Whoever holds a token can read your data until it expires or the key stops working. Disconnecting in the client does not invalidate it server-side (there is no database). Revoking the Personal API key at Garmin does not cut access at once either: the server keeps using the Garmin session (JWT) it cached for the key, so access ends only when that cached session expires, which can take up to 24 hours after the key is revoked at Garmin. To stop access, revoke the key at Garmin **and** disconnect the connector in the client. The operator can also remove the token keys the tokens were sealed with (which signs everyone out).
- **Session length.** Access tokens last 1 h; refresh tokens 720 h idle; an absolute session limit of 90 days by default (`oauth.max_session_ttl`). The login form can let you choose a shorter or longer session (`oauth.session_choices`), possibly one with no fixed end: nothing revokes such a session; it ends once its refresh token goes unused for the idle lifetime, or within 24 hours of revoking the key at Garmin (see above). All configurable by the operator.
- **Garmin JWT.** The server exchanges your key for a Garmin JWT (valid 24 h) lazily and caches it in memory only, keyed by a hash of the key, until shortly before it expires. A restart drops the cache.
- **TLS proxy.** A reverse proxy that terminates TLS sees the key in the form submission and the bearer tokens. Only use an operator you trust.
- **Rate limits.** Every Garmin call leaves from the server's IP, so the server limits requests per IP on the OAuth endpoints, per IP (unauthenticated and invalid requests only) and per token on the MCP endpoint, caps concurrency per token and in flight, and caps the request body. Defaults are in [docs/configuration.md](docs/configuration.md).
- **Key checks on the login form.** Each submitted key costs one call to Garmin, so all key checks together are capped (300 a minute by default), and each network (an IPv4 /24 or an IPv6 /48) may submit only a few keys that Garmin rejects (5 a minute, bursts of 10); keys Garmin accepts cost the network nothing. Someone flooding the form with junk keys to block everyone's logins therefore needs dozens of networks rather than a handful of addresses (by default 60 networks to hold the shared cap down). The price: everyone in a network that has used up its budget, honest users behind the same NAT included, waits for it to refill, even with a good key.
- **Redirect URIs are allow-listed** (claude.ai, claude.com, ChatGPT by default). Authorization uses PKCE S256, dynamic client registration and single-use codes and refresh tokens (within the limits of the next point).
- **Single use lasts only as long as the process.** Refresh tokens are rotated: each refresh returns a new one and spends the old one. Presenting a spent refresh token again more than 30 seconds after it was first used counts as theft and revokes the whole login chain, the legitimate newest token included; within those 30 seconds it is treated as a retry (a client that lost the answer to a timeout, or refreshed twice at once) and gets a new pair, so theft in that window goes undetected. The record of spent codes, spent refresh tokens and revoked chains is kept in memory only. After a restart (every deploy is one), any refresh token that has not yet expired is accepted again, including spent ones and those of chains revoked for theft, until its own expiry (720 h idle by default); an authorization code likewise within its 5 minutes. After a suspected token leak, restarting is no remedy: the operator must rotate the token keys (add a new first key, remove the old one, restart), which signs everyone out.
- **Read-only.** Every tool is annotated read-only and only issues GET requests to Garmin.
- **Hygiene.** This repository contains no real keys, tokens or user data; CI scans for secrets with gitleaks and blocks credentials and local agent files from being tracked.

## Testing

`make test` runs everything offline: unit tests plus `internal/e2e`, which drives the full OAuth flow and all 17 tools over HTTP against a fake Garmin and checks tool output sizes and range limits. It never touches the network or `test/api.key`.

Live tests against Garmin are opt-in. With your own Personal API key in `test/api.key` (git-ignored; override the path with `GARMIN_API_KEY_FILE`):

```sh
make live-test   # GARMIN_LIVE=1 go test ./internal/e2e/ -run Live -v
```

It calls every tool against the real API and prints only the tool name, an outcome (ok, no-data, tool-error) and the result size, never payloads, keys or tokens. Without `GARMIN_LIVE=1` or the key file it skips.

## Development

```sh
make build   # bin/garmin-papi-mcp
make test    # go test -race ./...
make vet
```

Releases: [docs/releasing.md](docs/releasing.md). License: see [LICENSE](LICENSE).
