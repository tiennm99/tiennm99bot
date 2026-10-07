---
title: Plan rebrand to tiennm99bot
date: 2026-10-07
summary: "Scouted and planned the miti99bot to tiennm99bot rebrand across code, repo, Coolify, Mongo and Telegram"
---

# Plan rebrand to tiennm99bot

## What happened
Scouted 159 files with `miti99bot`; most are the Go module path (315 import lines). External surfaces: GitHub repo, Coolify app on miti-sg (uuid ofo63lqv73huw1hce24ntg9i), Atlas DB, Telegram bot account.

## Decision
New @tiennm99bot account (bot usernames cannot be renamed), Mongo DB renamed by dump/restore, new sticker slug (proposed `stickers`), checkout moved, Coolify app renamed. Plan: plans/261007-1423-rebrand-tiennm99bot/.

## Next steps
Commit the pending BOT_USERNAME change, run phase 1 (code), then the phase 2 cutover with the user's BotFather/Atlas/Coolify steps.

> Historical work record — not durable authority. Prefer docs/specs/ADRs for current decisions.
