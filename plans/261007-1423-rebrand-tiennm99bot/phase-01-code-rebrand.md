---
phase: 1
title: "Code rebrand"
status: done
owner: agent
---

# Phase 1 — Code rebrand

## Context

Every in-repo `miti99bot` occurrence, listed in the
[scout report](../reports/scout-261007-1423-rebrand-tiennm99bot.md). The bot's
own username is already runtime-resolved (`BOT_USERNAME` or getMe), so nothing
here depends on the new Telegram account.

## Steps

1. **Go module path.** In `go.mod`, change it to `github.com/tiennm99/tiennm99bot`,
   then rewrite every import:
   `git ls-files '*.go' | xargs sed -i 's#github.com/tiennm99/miti99bot#github.com/tiennm99/tiennm99bot#g'`.
   `util/help.go` `repoURL` and `util/help_test.go` follow from the same sed.
2. **Runtime strings** → `tiennm99bot`:
   - `internal/server/health.go` health body, plus the `compose.yml` and
     `docs/deploy-coolify-selfhosted.md` text that quotes it.
   - `internal/deploynotify/deploy_notify.go` DM text, plus its test.
   - User-Agents in `coin/price_providers.go`, `gold/vnappmob_client.go` (×2),
     `stock/prices_ssi.go`, `lol/api_client.go` (`userAgentProduct`), plus
     `stock/prices_test.go` and `lol/api_client_test.go`.
   - `monkeyd/export_job.go` `cacheDirName`.
   - `renderer/src/gacha/page/page.js` edition label.
3. **Sticker slug.** Set `sticker/sticker_pack.go` `defaultStickerPackSlug` to the
   confirmed slug (proposed `stickers`). Update the comment example and the tests
   (`addsticker_command_test.go`, `sticker_pack_test.go`), plus the examples in
   `docs/sticker-packs.md`, `.env.example` and `compose.yml`.
4. **Test fixtures.** Change `/cmd@miti99bot` to `@tiennm99bot` in
   `modules/dispatcher_test.go` and `alias/fallback_test.go`, and the comment in
   `stats/views.go`. Change the test DB prefixes `miti99bot_*` to
   `tiennm99bot_*` in four `*_mongo*_test.go` files. Update the
   `sticker_pack_test.go` username fixture. Leave `misc/handlers_test.go`
   `@miti99` alone, because it is a user handle.
5. **loldle stickers.** Update the `loldle/stickers.go` comment to name
   @tiennm99bot. The file_ids are replaced in phase 2, because they need the
   new bot.
6. **Build/deploy config.**
   - `.github/workflows/ci.yml` image tags `tiennm99bot`, `tiennm99bot-renderer`.
   - `compose.yml`: the commented GHCR image and the DB example.
   - `.env.example`: the header and `MONGO_DATABASE=tiennm99bot`.
   - `renderer/package.json` name `tiennm99bot-renderer`. Regenerate the lock
     file with `npm install --package-lock-only` in `renderer/`; do not edit it
     by hand.
7. **Docs.**
   - `README.md` (title, local mongo container name, `_dev` DB), `AGENTS.md`,
     `CLAUDE.md`, `docs/deploy-coolify-selfhosted.md`.
   - `renderer/README.md`, `renderer/docs/deployment.md`.
   - `git mv renderer/docs/miti99bot-integration.md renderer/docs/tiennm99bot-integration.md`,
     then fix its links (`git grep -n miti99bot-integration`).
8. **Sweep.** `git grep -n -i miti99bot -- ':!plans'` must be empty. Review any
   remaining `miti99` hits by hand.

## Validation

```sh
gofmt -l . && go vet ./... && go test -race -count=1 ./... && go build ./...
(cd renderer && npm ci && npm run lint && npm run typecheck && npm test)
docker build -t tiennm99bot . && docker build -t tiennm99bot-renderer renderer
```

## Commit

Use two focused conventional commits on `main`:
- `feat(config): resolve the bot username from BOT_USERNAME or getMe` (the existing uncommitted change)
- `refactor!: rebrand miti99bot to tiennm99bot`

The body notes that the default sticker pack and the module path changed.
Delay the push to the cutover in phase 2 if the old bot should keep its old strings.

## Risk and rollback

- Go module path change: purely mechanical, and the build and tests catch any miss.
- Pushing deploys to Coolify. If it is pushed before cutover, the only visible
  effects are new strings and a new default pack name on the *old* bot, where
  `/addsticker` would create `stickers_by_miti99bot`. To avoid that, set
  `STICKER_PACK_NAME=miti99_by_miti99bot` in Coolify until cutover, or hold the push.
- Rollback: `git revert` the rebrand commit.
