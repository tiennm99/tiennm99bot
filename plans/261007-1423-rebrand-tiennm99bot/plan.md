---
title: "Rebrand miti99bot to tiennm99bot"
description: "Rename the code, repo, image, Coolify app, Mongo database and Telegram bot from miti99bot to tiennm99bot."
status: in-progress
priority: P2
effort: 1d (2h code, rest is a cutover window plus manual Telegram/Atlas steps)
branch: main
tags: [rebrand, deploy, mongo, telegram]
blockedBy: []
blocks: []
created: 2026-10-07
---

# Rebrand miti99bot → tiennm99bot

## Outcome

The project, the GitHub repo, the Coolify app, the Mongo database and the
Telegram bot are all named `tiennm99bot`. The bot runs as the new @tiennm99bot
account with all existing data. Old @miti99bot is retired after a migration
window.

Scout: [scout report](../reports/scout-261007-1423-rebrand-tiennm99bot.md).

## Decisions (user, 2026-10-07)

| Topic | Decision |
|---|---|
| Telegram bot | New @tiennm99bot account (new token). Bot usernames cannot be renamed. |
| Mongo database | Rename `miti99bot` → `tiennm99bot` by dump/restore, with a backup kept. |
| Sticker pack | New slug `stickers` → `stickers_by_tiennm99bot` (confirmed). |
| Push timing | Hold every push to `main` until the phase 2 cutover window. |
| Local checkout | Move to `/workspace/tiennm99/tiennm99bot`. |
| Coolify app | Rename the app on miti-sg and repoint it to the new repo. |

## Constraints and non-goals

- `plans/**` stays untouched: those are historical records.
- `@miti99` in `misc/handlers_test.go` is a user handle in a fixture, not the brand.
- Owner/admin IDs are Telegram *user* IDs and do not change.
- No data model changes. Chat IDs stored in Mongo stay valid under the new bot.
- Prerequisite: the uncommitted `BOT_USERNAME` change lands first as its own commit.

## Phases

| # | Phase | Who | Status |
|---|---|---|---|
| 1 | [Code rebrand](phase-01-code-rebrand.md) | agent | done (79104ef, 7008a57; not pushed) |
| 2 | [Cutover and manual steps](phase-02-cutover-and-manual-steps.md) | user + agent | B, C done 2026-10-07; D (retire old bot, drop old DB) after the migration window |

Phase 1 can merge to `main` any time, because nothing in it depends on the new
bot, repo or DB. Its deploy only changes branding strings and the default
sticker pack name, though. Hold the push until the phase 2 cutover window if
the old bot should keep its old strings until the switch.

## Acceptance criteria

- `git grep -i miti99bot -- ':!plans'` returns nothing.
- `go vet ./...`, `go test -race ./...`, `go build ./...`, the renderer
  `npm run lint && npm run typecheck && npm test`, and both docker builds pass.
- Coolify app `tiennm99bot` is running:healthy from `github.com/tiennm99/tiennm99bot`.
- The logs show `bot username ... tiennm99bot`, and the owner receives
  "🚀 tiennm99bot deployed: <sha>" from @tiennm99bot.
- Every collection in DB `tiennm99bot` has the same document count as in `miti99bot` at cutover.
- `/addsticker` creates or extends `stickers_by_tiennm99bot`, the loldle win/lose
  stickers send, and the inline `@tiennm99bot` alias picker works.

## Open questions

- Should old @miti99bot stay alive during the migration window to answer
  "moved to @tiennm99bot"? That would need a tiny separate process, so this
  plan leaves it idle instead. Telegram allows only one `getUpdates` consumer
  per token, so the old token is simply unused.
