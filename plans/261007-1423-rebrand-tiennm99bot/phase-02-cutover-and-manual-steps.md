---
phase: 2
title: "Cutover and manual steps"
status: in-progress
owner: user + agent
blockedBy: [phase-01]
---

# Phase 2 — Cutover and manual steps

Legend: **[you]** only you can do it (BotFather, Atlas UI, Coolify secrets).
**[agent]** I can run it once you approve that step.

Verified facts (2026-10-07):
- The Coolify app `miti99bot` is on **miti-sg** (uuid `ofo63lqv73huw1hce24ntg9i`).
- It has no `STICKER_PACK_NAME` and no `BOT_USERNAME` set.
- The name `tiennm99/tiennm99bot` is free on GitHub.
- `mongodump`/`mongorestore` are not installed here, but the local `mongo:8`
  image ships them.

## A. Prepare (no downtime)

1. **[you] Create the bot.** In BotFather, run `/newbot` → username `tiennm99bot`
   and keep the token. Then:
   - `/setinline` on @tiennm99bot. The alias inline picker needs it
     ([docs/aliases.md](../../docs/aliases.md)).
   - Optionally set `/setdescription`, `/setabouttext`, `/setuserpic`, and
     `/setjoingroups` (keep enabled).
   - The command menu needs nothing here: the bot registers it at startup.
2. **[you] Start the new bot.** Open @tiennm99bot from the `OWNER_ID` account
   and press Start. Without that, the deploy DM fails with 403. Admins should
   do the same.
3. **[you] Create the Atlas DB user.** Make a user with `readWrite` on database
   `tiennm99bot` only, and build the new `MONGO_URL` with it. Keep the old user
   until step D.
4. ✅ **Done 2026-10-07.** **[agent] Rename the repo.** Run `gh repo rename tiennm99bot -R tiennm99/miti99bot`.
   GitHub keeps a redirect from the old URL. Then set the local `origin` to
   `https://github.com/tiennm99/tiennm99bot.git`.
5. ✅ **Done 2026-10-07.** **[agent] Move the checkout.** Move `/workspace/tiennm99/miti99bot` to
   `/workspace/tiennm99/tiennm99bot`. `/tiennm99/` is already ignored at the
   workspace root. Restart the Claude session from the new path, because
   project memory is keyed by path.

## B. Cutover window (bot offline about 10–15 min)

1. **[agent] Stop the app.** Use `control stop` on the Coolify app (confirm
   required) so nothing writes during the copy.
2. **[agent] Back up and copy the database**, using the `mongo:8` image, with
   the URL passed via env and never echoed:
   - `mongodump --uri "$OLD_URL" --db miti99bot --archive=miti99bot-<date>.archive --gzip`
     Keep this archive outside the repo as the backup.
   - `mongorestore --uri "$NEW_URL" --archive=... --gzip --nsFrom 'miti99bot.*' --nsTo 'tiennm99bot.*'`
     This restores indexes too.
   - Compare `countDocuments` for every collection in both DBs with `mongosh`.
     Any mismatch means stop and fix before going further.
   - A first copy already ran on 2026-10-07 15:10 from `.env` `OLD_MONGO_URL`
     / `OLD_MONGO_DATABASE` (249 docs, counts and indexes match; backup
     `~/backups/mongo/miti99bot-20261007-1510.archive.gz`). The old bot kept
     writing after it, so at cutover use `--drop` on the restore to re-sync.
   - Docker bind mounts land on the daemon host, not this workspace: stream
     with `--archive` to stdout/stdin instead of `--out`.
3. **[you] Update the Coolify app env** in the dashboard (the MCP never writes
   secret values):
   - `TELEGRAM_BOT_TOKEN` = the new token
   - `MONGO_URL` = the new user's URL
   - `MONGO_DATABASE=tiennm99bot`
   - Optionally `BOT_USERNAME=tiennm99bot`, which skips getMe.
   - Do **not** set `STICKER_PACK_NAME`, so the default `stickers_by_tiennm99bot`
     applies.
   - Apply the same values to the preview copies, or delete those.
4. **[you] Rename and repoint the app.** Rename the Coolify app to `tiennm99bot`
   and set its git repository to `tiennm99/tiennm99bot`, branch `main`. Check
   that the GitHub App source still sees the repo after the rename.
5. **[agent] Deploy.** Push the phase 1 commits to `main` if they are held, or
   trigger `deploy`. Then watch `get_deployment` until it is running:healthy.

## C. Verify (agent checks plus you in Telegram)

1. **[agent]** The logs show `bot username ... source getMe|BOT_USERNAME` = `tiennm99bot`,
   `webhook cleared`, `telegram long polling started`, and no Mongo errors.
2. **[you]** The owner receives "🚀 tiennm99bot deployed: <sha>" from @tiennm99bot.
3. **[you]** Smoke-test in DM and in one group:
   - `/help`
   - `/stats` (old counts present, which proves the data moved)
   - `/lol`, `/stock`, `/gold`, `/coin`
   - `@tiennm99bot` inline
   - `/wheelofnames`
4. **[you]** Run `/addsticker` replying to a sticker. It creates
   `stickers_by_tiennm99bot`. The old `miti99_by_miti99bot` pack stays with the
   old bot. Re-adding its stickers to the new pack is manual, one `/addsticker`
   each.
5. **[you] Recapture the loldle stickers.** Send each wanted sticker to
   @tiennm99bot and capture its file_id with `/stickerid`. **[agent]** puts
   them in `internal/modules/loldle/stickers.go`, then commits and deploys.
   Until then, loldle simply sends no sticker, because the errors are already
   ignored.

## D. Migration window and cleanup

1. **[you] Move users and groups:**
   - Add @tiennm99bot to every group that used @miti99bot.
   - Users must Start the new bot in DM.
   - Stored subscriptions (lol daily push and others) are keyed by chat ID, so
     they stay valid. They deliver once the new bot is in that chat; until
     then, the fan-out logs 403s for those chats.
   - Announce the move from @miti99bot. Since its token is unused, send the
     announcement manually from your account, or post a message in each group.
2. **[you] Retire the old bot** after the window (suggested 2–4 weeks).
   Options: `/revoke` the old token in BotFather, `/deletebot`, or keep the
   name parked to stop impersonation (recommended: keep it parked and revoke
   the token).
3. **[agent] Drop the old database** after verification and the window. Run
   `db.getSiblingDB('miti99bot').dropDatabase()` only after a fresh count
   check, and keep the dump archive. **[you]** then delete the old Atlas user.
4. **[agent] Clean up.** Delete the local `miti99bot` and `miti99bot-renderer`
   docker images if any remain. Update workspace memory or notes that mention
   the old name.

## Rollback

Before D.3, rollback is quick:
1. Restore the old `TELEGRAM_BOT_TOKEN`, `MONGO_URL` and `MONGO_DATABASE`.
2. `git revert` the rebrand commit.
3. Redeploy.

The old DB is untouched until D.3. Any writes made to the new DB after cutover
would be lost on rollback.

## File_id migration (done 2026-10-07 16:10)

- Sticker file_ids turned out to work across bots: the 6 sticker aliases and
  all 6 loldle stickers send from @tiennm99bot unchanged, so C.5 needs no code
  change. Photo and animation file_ids do not transfer.
- The 9 photo/animation aliases were downloaded with the old token, re-uploaded
  through the new bot to the owner DM (messages deleted), send-tested, and
  their `fileId` updated only where unchanged. Three GIFs were stored without
  a file extension and had to be uploaded as `.mp4` to stay animations.
- The 8 stickers of `miti99_by_miti99bot` were added to
  `stickers_by_tiennm99bot` by file_id (pack now 9 with the /addsticker test).
- Backup before the update: `~/backups/mongo/tiennm99bot-20261007-pre-fileid-migration.archive.gz`.
