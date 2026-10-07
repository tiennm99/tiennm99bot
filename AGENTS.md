# AGENTS.md

## Project Context

`tiennm99bot` is a Go Telegram bot with pluggable modules under
`internal/modules`. Runtime storage is MongoDB when `MONGO_URL` is set and
in-memory otherwise, which is what tests and local no-database runs use. Read
`README.md` before implementation work.

The `monkeyd` module's crawler, PDF renderer, and export flow are an in-tree
port of `tiennm99/mttools/monkeyd-crawler` under
`internal/modules/monkeyd/{crawler,pdf,export}`. Change them here; they no
longer track the mttools copy.

`renderer/` is a separate Node 24 service (JavaScript + JSDoc, Remotion) that
draws the `random` module's animations. It has its own `package.json`, tests,
and `renderer/AGENTS.md`; run its npm commands from that folder. `compose.yml`
deploys it next to the bot.

## Development Rules

- Keep changes scoped to the requested module or shared contract.
- Follow existing module patterns before adding abstractions.
- Use `rg` for code search.
- Use `gofmt` on changed Go files.
- Run focused tests for touched packages, then `go test ./...` and `go vet ./...`
  for command, storage, migration, or shared behavior changes.
- Before committing code changes, run the CI lint gate locally with
  `golangci-lint run` when the binary is available.
- Do not commit secrets, tokens, dotenv files, private keys, or production data.

## Command Changes

Telegram command names are user-facing contracts. When adding, renaming, or
deleting commands, update all related surfaces:

- module command registration in `internal/modules/<module>/`
- command parameter metadata used by Telegram and `/help`
- handler usage text and user-facing error text
- tests for registration, handlers, and command menu behavior
- the README module table, plus the feature doc under `docs/` when one exists,
  when behavior changes are user-visible

Follow `docs/command-parameter-conventions.md` for all command parameter
metadata and usage text. Keep metadata, usage errors, examples, and tests exact.
Telegram's native menu and `/help` show command syntax plus the summary without
example invocations.

## Stats Compatibility

The `stats` module persists command usage in the `stats` collection. Command
name changes must preserve stats history.

- When renaming a command, add a one-time startup migration that moves stats
  from the old command name to the new command name. Cover anonymous command
  totals and per-user command rows. Guard the migration with the shared
  `system` collection so it is idempotent.
- When deleting a command, do not delete its stats rows. Keep them as legacy
  records and mark them with `deleted: true` in the stats document.
- Stats queries must filter legacy deleted rows from visible results. Apply the
  filter consistently to top commands, top users, commands by user, users by
  command, and username lookup paths for both MongoDB and in-memory stores.
- If adding a deleted marker or migration fields, update indexes if query
  performance needs it and add tests for both startup migration and stats views.
- Legacy stats records are retained until the project owner decides to remove
  them.

## Startup Migrations

Startup migrations should be safe to run every boot:

- create MongoDB indexes idempotently
- use `internal/systemstate` records in the shared `system` collection for
  one-time migrations
- write tests for migration idempotency and legacy data handling
- after production data is verified migrated and the owner approves cleanup,
  remove completed one-time migration runtime code and migration-only tests;
  keep historical `system` marker records and legacy data unless the owner
  explicitly asks to delete them

## Git

Use conventional commit messages without AI attribution. Keep commits focused;
split unrelated code, test, docs, and config changes when useful.
