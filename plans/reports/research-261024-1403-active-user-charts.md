# Research: Daily, weekly and monthly active-user charts

Conducted 2026-10-24 (Asia/Saigon) for `tiennm99/tiennm99bot`.

## Answer

The bot cannot draw DAU, WAU or MAU history today. The `stats` collection
keeps running totals per (command, user) and does not record when each use
happened. Telegram gives bot owners no API for active users. So charts need a
small new activity record, a daily rollup, and an in-process PNG chart sent
with `sendPhoto`. Recommended stack:

1. **Record:** one MongoDB document per (ICT day, user ID), upserted on each
   interaction.
2. **Roll up:** a cron job after midnight ICT writes one `{day, dau, wau, mau}`
   document per day. WAU and MAU are rolling 7-day and 30-day distinct counts.
3. **Chart:** `go-analyze/charts` renders a pure-Go PNG line chart for an
   admin command such as `/stats active [30|90|365]`.

History starts on the day the record ships. It cannot be backfilled.

## What exists today

- `internal/modules/stats/stats.go` `Inc` only counts users who have a public
  username. Everyone else goes into an anonymous bucket for each command, so
  their IDs are never stored. Active-user counts need every sender's ID.
- The hook only sees `update.Message`. Callback queries, inline queries and
  web-game plays (the `wordledaily`, `loldle` and `noitu` HTTP APIs) are not
  counted. Since the games moved to web pages, many real players may never send
  a command.
- Each row has an `updatedAt` field with its last-touch time. A query for rows
  with `updatedAt` in the last 30 days gives a rough current MAU, limited to
  users with a username. It cannot rebuild any past days.

## Telegram's own numbers

Telegram has no Bot API method that returns active users. A third-party
analysis says bot profiles show monthly users once a bot passes about 10,000
MAU, and that Mini App activity may not count toward it. A separate Telegram
announcement says profiles of bots with Mini Apps show the Mini App's monthly
unique users. Neither source is official documentation, and neither applies
at this bot's scale. Telemetree sells MAU estimates for public bots. It is an
outside estimate, not the owner's data. **You need to count users yourself.**

## Data model

```text
activity       _id "2026-10-24:123456789"  { day: "2026-10-24", uid: 123456789 }
activity_daily _id "2026-10-24"            { dau, wau, mau, computedAt }
```

- Use `$setOnInsert` with upsert. A repeat on the same day changes nothing, so
  one write per user per day is the only real cost.
- Compute the day key in `Asia/Ho_Chi_Minh` so it lines up with the 07:00 ICT
  daily-game reset and the existing cron jobs.
- Store only the user ID and the day: no username and no chat. That is less
  identifying than the current `stats` rows.
- Size: users × active days. Even 1,000 daily users for a year is about 365k
  tiny documents, well within the Atlas free tier. You could add a TTL on raw
  rows (for example 400 days) because the rollup keeps the history. Since
  `day` is a string, TTL would need an extra `createdAt` date field.

## Computing the metrics

- **DAU(d):** `countDocuments({day: d})`.
- **WAU(d) and MAU(d), rolling:** distinct `uid` where `day` falls in
  `[d-6, d]` or `[d-29, d]`. Use `$match` on a `day` range, `$group` by `uid`,
  then `$count`. An index on `{day: 1, uid: 1}` covers it.
- The nightly cron computes yesterday's row. A one-off loop over the stored
  days fills any gaps. It is simpler to do this in Go, one aggregate per day,
  than in a single pipeline. MongoDB's `$setWindowFields` does support
  `$addToSet` with `range` and `unit: "day"`, but it collects one set per
  input document and then needs `$setUnion` reshaping. That is more complex
  for no gain at this size.
- Calendar-month MAU (October and so on) is the same query with month bounds.
  A rolling 30-day window gives a smoother line and is the usual convention.
- **Stickiness**, DAU divided by MAU, is cheap to add as a fourth series or
  a single number in the caption.

For a small bot, the DAU line will be noisy. Rolling WAU is usually the most
readable trend, which matches your preference for daily or weekly charts. Plot
all three lines on one chart.

## Chart rendering options

| Option | Status (checked 2026-10-24) | Fit |
| --- | --- | --- |
| `go-analyze/charts` | Active (pushed 2026-10-23), MIT, deps are `golang/freetype` and `x/image` (pure Go, CGO off is fine) | **Recommended.** PNG bytes in-process, line charts, themes. Fork of the two archived libraries below |
| `wcharczuk/go-chart` | Archived 2024-08 | Avoid |
| `vicanso/go-charts` | Archived 2024-08 | Avoid |
| `gonum/plot` | Active | Works, but its plots are built for science and look plain. Date axes take more code |
| `go-echarts` | Active | HTML only, no PNG. Could fit a web page served next to the games |
| QuickChart.io | Free, 120 req/min per IP; self-hosting means GPLv3 and a sandbox for arbitrary Chart.js JavaScript | Sends user counts to a third party, or adds another container. No benefit over in-process |
| Existing `renderer` (Remotion) | In repo | Could render a chart, but Node and a network hop are overkill for a static PNG |
| Metabase or Grafana on Coolify | Heavy | Only worth it if you want a general analytics dashboard beyond this chart |

Flow: an admin runs `/stats active 90`. The handler reads `activity_daily`
for the range, renders the PNG, and calls `sendPhoto` with a caption giving
the latest DAU, WAU and MAU.

## Next steps

1. Decide what counts as active (see questions below).
2. Add the `activity` record next to the existing `stats` hook, or as one
   update-level middleware if callbacks and web plays should count.
3. Add the rollup cron (about 00:10 ICT) and the `{day: 1, uid: 1}` index.
4. Add `/stats active [days]` (admin) using `go-analyze/charts`, then update
   the README `stats` row.
5. Start collecting now. The chart becomes useful after about 30 days.

## Sources

- [MongoDB `$setWindowFields` reference](https://www.mongodb.com/docs/manual/reference/operator/aggregation/setWindowFields/): `$addToSet` and day-unit ranges are supported
- [Cube.dev active-users recipe](https://cube.dev/docs/recipes/active-users): rolling-window distinct-count convention
- [go-analyze/charts](https://github.com/go-analyze/charts), [wcharczuk/go-chart](https://github.com/wcharczuk/go-chart), [vicanso/go-charts](https://github.com/vicanso/go-charts), [gonum/plot](https://github.com/gonum/plot): status read via `gh api`
- [typpo/quickchart](https://github.com/typpo/quickchart): license, rate limit, sandbox warning
- [Telemetree MAU API](https://docs.telemetree.io/api-reference/monthly-active-users-api/monthly-active-users-overview)
- [Telegram bot trends, August 2026 (third party)](https://okhlopkov.com/en/articles/telegram-bot-trends-august-2026/) and [Telegram Info repost on Mini App MAU](https://tx.me/s/tginfoen?after=1942): unofficial

## Unresolved questions

- What counts as "active"? Commands only, or also callback buttons, inline
  queries and web-game plays? The web games are likely where most activity is.
- Should users without a username count? For active-user numbers they
  should, by ID only. That differs from the current `stats` privacy choice.
- Should group activity be tracked separately, such as active chats per day,
  or only users?
