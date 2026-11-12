# Active-user charts (research, not implemented)

Researched 2026-10-24; the full report is in git at commit `98387fc`
(`plans/reports/research-261024-1403-active-user-charts.md`).

- **Finding:** the bot cannot chart DAU, WAU or MAU. The `stats` collection
  keeps running totals per (command, user) with no per-day record, skips
  users without a username, and only sees messages, not callbacks or web-game
  plays. Telegram offers no active-user API.
- **Proposed design:** one MongoDB document per (ICT day, user ID), upserted
  on every interaction; a cron rollup after midnight ICT writing
  `{day, dau, wau, mau}` (rolling 7- and 30-day distinct counts); an admin
  `/stats active [days]` command drawing a PNG line chart with the pure-Go
  `go-analyze/charts`.
- **Open questions:** what counts as active (commands only, or also
  callbacks and web-game plays), and whether to start collecting before the
  chart exists. History cannot be backfilled; it starts when the record ships.
