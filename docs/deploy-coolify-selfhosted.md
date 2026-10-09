# Deploy: Self-host (Coolify + MongoDB Atlas)

Run `tiennm99bot` as a long-lived container on [Coolify](https://coolify.io) with
[MongoDB Atlas](https://www.mongodb.com/atlas) (free M0) for storage.

## Architecture

```
   Telegram  <── long poll (getUpdates) ──  container  (outbound only)
   in-process scheduler ───────────────────> module crons
   MongoDB Atlas (db / one collection per module + system metadata)
   Coolify env vars (plain secrets)
   no ingress for Telegram (polling = outbound only; no /webhook)
   optional HTTPS ingress ──> container :8080 /games/noitu/ (only when GAME_BASE_URL is set)
```

- **Storage** — `mongodb` auto-selected when `MONGO_URL` is set (no `KV_PROVIDER`).
- **Cron** — an in-process scheduler (`internal/cron`) runs unconditionally and
  fires each module cron on its `Schedule`, evaluated in UTC. Examples: the
  `lol` daily digest at `0 1 * * *` (08:00 ICT), and the `noitu` game's
  minutely session sweep when the game is enabled.
- **Transport** — long polling (`b.Start`) is the **only** transport. The bot
  opens an outbound connection to Telegram and pulls updates, so there is no
  public domain, no `/webhook`, and no webhook secret. The container clears any
  leftover webhook on startup (`deleteWebhook`) before polling.
- **Game page** — the `noitu` HTML5 game is the one thing served to the
  public. With `GAME_BASE_URL` set, the bot's HTTP server on `:8080` serves the
  game page and its JSON API under `/games/noitu/`, and that path must be
  reachable over HTTPS from players' phones. With it unset, those routes do not
  exist and the bot needs no public ingress at all. See
  [the noitu game](noitu-game.md).

## Environment

Copy [`.env.example`](../.env.example) → `.env` (gitignored) and fill in.

| Var | Required | Notes |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | ✅ | from @BotFather; startup fails without it |
| `MONGO_URL` | ✅ | Atlas SRV string **incl. credentials** — secret, never logged |
| `MONGO_DATABASE` | ✅ | e.g. `tiennm99bot` |
| `MODULES` | optional | CSV; empty = all modules, including any added later |
| `OWNER_ID` | optional | Telegram user id for owner-only commands, the deploy DM, and the `/addsticker` pack owner. Unset = owner-only commands are denied and `/addsticker` refuses |
| `ADMIN_IDS` | optional | CSV of Telegram user ids for admin-only commands |
| `BOT_USERNAME` | optional | the bot's Telegram username, without `@`; unset = asked from Telegram (`getMe`) once at startup. Used for the default sticker pack name and the lol User-Agent |
| `STICKER_PACK_NAME` | optional | set `/addsticker` writes to; default `stickers_by_<bot username>`. See [sticker packs](sticker-packs.md) |
| `LOL_PANDASCORE_TOKEN` | optional | PandaScore API token for the lol module (free tier) — secret, never logged; without it every `/lol*` fetch fails (stale cache may still serve briefly) |
| `GAME_BASE_URL` | optional | public `https://` base routed to the bot's `:8080`, e.g. `https://noitu.example.com`; unset or invalid = the `noitu` and `wordledaily` games are disabled. See [the noitu game](noitu-game.md) and [Wordle Daily](wordledaily.md) |
| `NOITU_GAME_SECRET` | optional | at least 32 bytes; signs game links — secret. Unset = derived from `TELEGRAM_BOT_TOKEN`, so rotating the token invalidates open game links |
| `WORDLEDAILY_GAME_SECRET` | optional | at least 32 bytes; signs `wordledaily` game links and keys its daily answer order — secret. Unset = derived from `TELEGRAM_BOT_TOKEN`. Puzzles already started keep their stored answer when it changes. See [Wordle Daily](wordledaily.md) |
| `RENDERER_URL` | leave unset | base URL of the animation renderer; fixed by `compose.yml` to the bundled renderer (`http://renderer:3000`), so a Coolify value is ignored |
| `LOG_LEVEL` | optional | `debug`, `info` (default), `warn`, or `error`; logs are JSON on stdout |
| `GOLD_VNAPP_API_KEY` | optional | VNAppMob key — secret; empty = the gold module fetches one and caches it in MongoDB |
| `KV_PROVIDER` | leave unset | `memory` or `mongodb`; unset = `mongodb` when `MONGO_URL` is set, otherwise `memory` |
| `PORT` | leave unset | health server (and game page) port; default `8080` |
| `SOURCE_COMMIT` | never set | provided by Coolify at runtime for the deploy DM (see step 5 below) |

`compose.yml` references only the required settings; every optional one is a
commented-out `${VAR:-default}` line, and the code falls back to that default.
Two Coolify behaviours, confirmed in its compose parser, shape this:

- Coolify writes every dashboard variable into a generated `.env` and adds
  `env_file: .env` to each service, so a variable set in the dashboard reaches
  the containers whether or not `compose.yml` mentions it. Set optional values
  in the dashboard; there is nothing to uncomment.
- Coolify creates a dashboard entry for every variable `compose.yml`
  references, and stores a `${VAR:-default}` default only when it first
  creates that entry. An existing entry — even an empty one — is written into
  the deployed compose as is, so the compose default never applies. Keeping
  optional variables unreferenced avoids such empty entries.

Outside Coolify, uncomment a line in `compose.yml` to pass that variable in.

Stock, coin, and gold provider URL overrides are not supported in runtime env;
modules use coded defaults. There is no `TELEGRAM_WEBHOOK_SECRET`: long polling
has no webhook.

> Cron runs in-process (`internal/cron`) — there is no `/cron` HTTP route and no
> `CRON_SHARED_SECRET`. The scheduler is the sole trigger; nothing inbound.

### Animation renderer

`/wheelofnames`, `/gacha`, and `/genshin` are drawn by the Node renderer in
[`renderer/`](../renderer/README.md) (Remotion and headless Chrome).
`compose.yml` deploys it as a second service, `renderer`, next to the bot. It
is internal only: the bot reaches it at `http://renderer:3000` over the
compose network, so it needs no domain, publishes no port, and takes no auth
token. Its API is unauthenticated, so never publish a port or attach a domain
to it.

Renderer tuning (`RENDERER_MAX_CONCURRENT_RENDERS`,
`RENDERER_RENDER_TIMEOUT_MS`, `RENDERER_MAX_OPTIONS`,
`RENDERER_MAX_OPTION_CHARS`) is optional; the renderer falls back to the
defaults listed in [`renderer/docs/deployment.md`](../renderer/docs/deployment.md).
Set them in the Coolify dashboard to override. Give the host
1-2 GB of headroom for the renderer's Chrome.

Outside compose, set `RENDERER_URL` to the base URL of any service that
implements the same `/api/gif`, `/api/gacha`, and `/api/genshin` routes, e.g.
`http://localhost:3000`. The bot appends each route itself.

The bot sends outbound HTTP only; no public bot ingress is required. Remote
renders use `512px`, `20fps`, and `7` seconds total by default. If the renderer
is unset, unavailable, or returns a non-GIF response,
`/wheelofnames` falls back to the same plain text winner reply as `/random`.
Successful GIF replies include the result behind Telegram spoiler formatting.

When a renderer is configured, the bot posts a `Spinning...` holding message
first, because the render takes several seconds. The GIF then replaces it; a
render or upload failure edits that same message into the plain text winner
instead. With no renderer configured there is no holding message — the winner
reply is immediate.

`/gacha` uses the same service at `/api/gacha`. It renders a 6-second
`360x640` portrait silent MP4 wish animation (a card pack torn open), posts `Wishing...` while it renders, and
falls back to a text reply such as `★★★★★ Pizza` on the same failures. Every
option is equally likely, as with `/random`; the rarity only sets what the
animation and reply show. Options are 5★ by default; prefix `4*` or `3*` to
lower one, e.g. `/gacha Pizza, 4* Pho, 3* Rice`.

The unlisted `/genshin` takes the same input and renders the Genshin-style
meteor wish from `.../api/genshin` on the same service as a 7-second `640x360`
MP4, with the same text fallback.

## 1. MongoDB Atlas (M0)

1. Create a free **M0** cluster (512 MB — ample for the tiny paper-trading KV).
2. **Database user (least privilege):** create a user with role
   **`readWrite` on the single app database only** (e.g. `tiennm99bot`) — never
   Atlas admin or cluster-wide. Use a **strong unique password**.
3. **Network access:** add `0.0.0.0/0`.

   > **Accepted trade-off.** The Coolify host has no stable
   > egress IP, so the Atlas IP allow-list is open to the internet. This widens
   > the database surface. The mandatory compensating controls are: (1) strong
   > unique password, (2) least-privilege `readWrite`-on-one-db user, (3) the
   > connection string is a secret and is never logged (the bot logs only the
   > database name on startup).

4. Copy the `mongodb+srv://…` connection string into `MONGO_URL` and put the
   db name in `MONGO_DATABASE`.

### Storage layout

- **One collection per module.** Each document is a flattened native document
  — `{ _id: <user key>, ...payload fields, version, updatedAt }` with no `value`
  envelope. Payload fields are hoisted to the document root so they expand and
  are queryable in Compass. The two non-object values are wrapped in a named
  field: `lol` schedule subscribers under `subscribers` (array) and the daily
  push date under `date`. Concurrency uses the `version` field (optimistic
  lock); `updatedAt` is a BSON Date.
- **`stats`** uses queryable aggregate documents for command/user counts and
  creates its indexes on startup. Deleted legacy command rows are retained with
  `deleted: true`, and `/stats` filters them from visible results.
- **`stock`** stores cash as `vnd`, embeds positions as
  `assets.<symbol>.{quantity,base,openedAt}`, and retains normalized per-user
  SSI dividend history under `dividends.<symbol>.<ssi_event_id>`. The
  [README](../README.md#stock-dividend-commands) describes how those records
  are replayed and expired.
- **`coin`** stores cash as `usd` and embeds positions as
  `assets.<symbol>.{quantity,base}`.
- **`system`** holds one marker per completed one-time startup migration. Keep
  those records as audit history. No one-time migration runs at startup now;
  the completed ones were removed from the code once production data was
  verified migrated.

## 2. Coolify

1. New resource → from this Git repo (Docker Compose), or a prebuilt image.
   The committed [`compose.yml`](../compose.yml) defines the `bot` service
   and the internal `renderer` service.
2. Set the env vars above in Coolify.
3. **No public domain / port** is needed for the bot itself — polling is
   outbound-only. Never publish a host port. `expose: 8080` keeps the health
   endpoint reachable only inside Coolify's network. **Only if the `noitu` game
   is enabled:** attach a domain to the `bot` service with port 8080 (in Coolify,
   `https://noitu.example.com:8080`), and set `GAME_BASE_URL` to that domain
   without the port. Coolify's proxy terminates TLS and forwards to the
   container's 8080; only `/games/noitu/`, `/games/wordledaily/` and the health text are served there.
   Never attach a domain to the `renderer` service.
4. **Exactly one replica.** Telegram permits only one `getUpdates` consumer per
   bot token; a second poller gets HTTP 409, and a second in-process scheduler
   double-fires crons. Prefer **stop-first redeploys** so two containers never
   overlap near a cron time.
5. **deploynotify commit SHA:** `SOURCE_COMMIT` is a Coolify predefined
   variable. The bot reads it at startup and DMs the owner on every boot;
   outside Coolify (local `docker compose up`) it is unset and the DM shows
   `unknown`. Keep "Include Source Commit in Build" disabled: that setting
   affects build args only, is not needed for this runtime path, and would
   invalidate Docker cache on every commit. Do not add `SOURCE_COMMIT` to
   `compose.yml`; an interpolated empty value can override Coolify's runtime
   env-file value.
6. **Health check:** Coolify's UI health-check settings do not apply to
   Docker Compose apps; Coolify reads each service's `healthcheck:` in
   `compose.yml` instead. The `bot` service checks `GET /` (returns
   `text/plain` `tiennm99bot ok`) with the image's busybox `wget`, and the
   `renderer` service checks `/api/healthz`. Note: `/` reports healthy even if
   Mongo is unreachable (the driver auto-reconnects on the next op); a DB
   outage will not mark the container unhealthy — accepted trade-off.

## 3. Command menu

The bot registers its Telegram command menu from the loaded modules' public
commands on every startup. The Go module registry is the single source of
truth, so no separate command-menu file or manual registration step is
required. See [Command discovery](../README.md#command-discovery) for how the
menu text is built.

## 4. Group setup

By default a bot in a group runs in privacy mode. It receives only commands
addressed to it, replies to its own messages, and service messages, and it
cannot read messages sent by other bots. That breaks two things:

- A bare `/command` typed as a reply to another bot's message is routed to that
  other bot. Address this bot explicitly, as `/command@<this bot>`.
- `/addsticker` and `/alias` replying to another bot's message (for example, an
  image another bot posted) get the reply with its content stripped.

To make both work in a group:

1. In @BotFather, enable **Bot-to-Bot Communication Mode** for this bot.
2. Promote the bot to **admin** in the group. Admins receive every message, so
   no permissions beyond the defaults are needed.

Disabling privacy mode in BotFather (`/setprivacy`) is the alternative to admin
rights, but Telegram applies it only after the bot is removed from the group and
added back. Admin rights take effect immediately. Verified on 2026-10-09:
`/addsticker@<this bot>` replying to another bot's image failed with privacy
mode off and the bot not re-added, and worked once the bot was promoted to admin.
See Telegram's [privacy mode and bot-to-bot rules](https://core.telegram.org/bots/features#privacy-mode).

## Operations

The live deployment is the Coolify container and MongoDB is the sole system of
record. Keep exactly one replica running. To confirm Telegram is in polling mode:

```sh
# POSIX shells (Linux/macOS)
curl "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getWebhookInfo"
```

```powershell
# PowerShell
Invoke-RestMethod "https://api.telegram.org/bot$env:TELEGRAM_BOT_TOKEN/getWebhookInfo"
```

`url` should be empty. If needed, clear the webhook explicitly:

```sh
# POSIX shells (Linux/macOS)
curl -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/deleteWebhook" \
  --data "drop_pending_updates=false"
```

```powershell
# PowerShell
Invoke-RestMethod -Method Post -Uri "https://api.telegram.org/bot$env:TELEGRAM_BOT_TOKEN/deleteWebhook" -Body @{ drop_pending_updates = "false" }
```

## Local smoke test

```powershell
# PowerShell
Copy-Item .env.example .env # fill TELEGRAM_BOT_TOKEN, MONGO_URL, MONGO_DATABASE
docker compose up --build
```

```sh
# POSIX shells (Linux/macOS)
cp .env.example .env # fill TELEGRAM_BOT_TOKEN, MONGO_URL, MONGO_DATABASE
docker compose up --build
```

Boot logs are JSON lines. Look for `"msg":"storage backend"` with
`"backend":"mongodb"` and the database name (never the connection string),
`"msg":"cron scheduler started"`, and `"msg":"telegram long polling started"`.
`compose.yml` does not publish port 8080 to the host, so check the health
endpoint from inside the container:

```sh
docker compose exec bot wget -qO- http://127.0.0.1:8080/
```

It returns `tiennm99bot ok`. The bot's webhook must be unset (the container
clears it on startup) or `getUpdates` 409s.
