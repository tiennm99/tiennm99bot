# Scout Report: rebrand miti99bot → tiennm99bot

Baseline: `main` @ c18006f plus the uncommitted `BOT_USERNAME` change (username
is now runtime-resolved; sticker default = `miti99_by_<bot username>`).

## In-repo occurrences (159 files)

### Go module path — 315 import lines, ~150 files
- `go.mod:1` `module github.com/tiennm99/miti99bot`
- Every `internal/...` import in `cmd/` and `internal/`. Mechanical rewrite.
- `internal/modules/util/help.go:16` `repoURL`; asserted in `util/help_test.go:62,165`.

### Runtime branding strings
- `internal/server/health.go:20` — `"miti99bot ok\n"` (health body; docs quote it).
- `internal/deploynotify/deploy_notify.go:74` — `"🚀 miti99bot deployed: %s"`; test `deploy_notify_test.go:89`.
- User-Agent `Mozilla/5.0 (miti99bot)`: `coin/price_providers.go:261`, `gold/vnappmob_client.go:127,233`, `stock/prices_ssi.go:152`; asserted in `stock/prices_test.go:66-67`.
- `lol/api_client.go` `userAgentProduct = "miti99bot/0.1"`; asserted in `lol/api_client_test.go` (`TestClientUserAgent`).
- `monkeyd/export_job.go:42` — cache dir `miti99bot-monkeyd-cache` (temp dir; rename is harmless, old dir orphaned).
- `renderer/src/gacha/page/page.js:281` — gacha recap edition label `'miti99bot'`.

### Sticker pack slug `miti99`
- `sticker/sticker_pack.go:29` `defaultStickerPackSlug = "miti99"`, comment at `:79`.
- Tests: `sticker/addsticker_command_test.go:108,136`, `sticker/sticker_pack_test.go:24`.
- Prod sets no `STICKER_PACK_NAME`, so the live pack is the derived default `miti99_by_miti99bot`.

### Test fixtures (bot-agnostic, rename for consistency only)
- `/cmd@miti99bot`: `modules/dispatcher_test.go:92,98,110,140`, `alias/fallback_test.go:43,45`, `stats/views.go:82` (comment).
- Test DB names `miti99bot_*_test_%d`: `storage/mongo_doc_store_test.go:38`, `lol/startup_mongo_test.go:32`, `stats/startup_mongo_test.go:129`, `stock/startup_mongo_test.go:79`.
- `sticker/sticker_pack_test.go:22-23`, `sticker/addsticker_command_test.go`.
- `misc/handlers_test.go:257-263` uses `@miti99` as a *user* handle — not the brand; leave.

### Bot-scoped data
- `loldle/stickers.go` — sticker file_ids valid only for the @miti99bot account (comment line 5).

### Build / deploy config
- `.github/workflows/ci.yml:65,102` — local image tags `miti99bot`, `miti99bot-renderer`.
- `compose.yml:6` (`ghcr.io/tiennm99/miti99bot:latest`, commented), `:12` DB example, `:53` health text.
- `.env.example:1,12` header and `MONGO_DATABASE=miti99bot`.
- `renderer/package.json:2`, `renderer/package-lock.json:2,8` — `miti99bot-renderer`.

### Docs
- `README.md:1,289,293,295`, `AGENTS.md:5`, `CLAUDE.md:1`.
- `docs/deploy-coolify-selfhosted.md:3,33,118,184,249`, `docs/sticker-packs.md:27,55`.
- `renderer/README.md:3,180,181`, `renderer/docs/deployment.md:3`, `renderer/docs/miti99bot-integration.md` (file name + 3 lines).
- `plans/**` — historical records; leave untouched.

## External surfaces (outside the repo)
- GitHub repo `tiennm99/miti99bot` (git `origin`). Renaming keeps a redirect.
- Coolify app `miti99bot` on **miti-sg** (uuid `ofo63lqv73huw1hce24ntg9i`, project "Applications", running:healthy). Env keys: `TELEGRAM_BOT_TOKEN`, `MONGO_URL`, `MONGO_DATABASE`, `OWNER_ID`, `ADMIN_IDS`, `MODULES`, `LOL_PANDASCORE_TOKEN` (+ preview copies). No `STICKER_PACK_NAME`, no `BOT_USERNAME`.
- MongoDB Atlas database (value of `MONGO_DATABASE`; docs suggest `miti99bot`) and its least-privilege user scoped to that DB.
- Telegram bot account @miti99bot. Bot usernames cannot be renamed; a new @tiennm99bot means a new bot + token.
- Local checkout path `/workspace/tiennm99/miti99bot` (workspace rule: `<owner>/<repo>`).
- No GHCR publish workflow exists; the image ref is only a comment.

## Unresolved Questions
- Is the Telegram bot itself moving to a new @tiennm99bot account, or only the code/repo?
- Should the Mongo database be renamed (requires dump/restore; Mongo has no DB rename)?
- Should the sticker pack slug change (`tiennm99_by_…`), creating a new pack?
