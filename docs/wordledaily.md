# Wordle Daily

`wordledaily` is a Telegram HTML5 game modelled on NYT Wordle and its Discord
Activity. Everyone plays the same English five-letter word each day. A new
puzzle starts at 07:00 ICT (00:00 UTC), and the puzzle number goes up by one
each day.

It has three commands:

- `/wordledaily` sends the game card in any chat, private or group, and in a
  forum topic it stays in that topic. Pressing **Play** opens a board page that
  this bot serves.
- `/wordledaily_subscribe` opts the chat, or the forum topic, into a daily push
  at 07:00 ICT.
- `/wordledaily_unsubscribe` opts it out.

A topic means a real forum topic (`is_topic_message`). Telegram also gives a
reply chain in an ordinary supergroup a thread ID; a command sent as such a
reply counts for the whole chat, the same scope its plays and group results
are stored under.

Anyone in a chat may subscribe or unsubscribe, the same as `/lol_subscribe` and
`/thuyvan_subscribe`. Channels are refused, because Telegram does not allow
games in channels.

The classic `/wordle` text game is a separate module. The two share their word
data and scoring through `internal/modules/wordle/wordlist`.

## Setup

1. **BotFather:** create a game for the bot with `/newgame` and the short name
   `wordledaily`. The share link is then `t.me/<bot username>?game=wordledaily`.
2. **Public URL:** this uses the same `GAME_BASE_URL` as the
   [noitu game](noitu-game.md#setup). The page is served at
   `<GAME_BASE_URL>/games/wordledaily/`.
3. **Environment:** `WORDLEDAILY_GAME_SECRET` is optional and must be at least
   32 bytes. Two keys are derived from it, each with its own label:
   - `tiennm99bot/wordledaily/token/v1` signs the Play links.
   - `tiennm99bot/wordledaily/answer/v1` orders the daily answers.

   When the secret is unset, the bot token is the root key. Because the labels
   are separate, a noitu link never opens this game.
4. Keep `wordledaily` in `MODULES`, or leave `MODULES` empty.

The game is disabled when any of these holds:

- `GAME_BASE_URL` is unset or invalid.
- The secret is shorter than 32 bytes.
- There is no storage.

A disabled game behaves like this:

- `/wordledaily` and `/wordledaily_subscribe` answer "Wordle Daily isn't
  configured on this server." `/wordledaily_unsubscribe` still removes a
  subscription.
- Play shows the same text as an alert.
- No `/games/wordledaily/` route exists and the daily push is not registered.

## The puzzle

- **Day boundary:** puzzle #1 started at 07:00 ICT on 9 October 2026. Puzzle `n`
  runs from `epoch + (n−1) days` until one day later. Because 07:00 ICT is
  00:00 UTC, the 00:00 UTC cron always pushes exactly the puzzle a player then
  gets.
- **Word lists:**
  - `wordlist/data/answers.txt` holds about 2,230 common English words that a
    puzzle can be. It was curated for this bot; it is not NYT's list.
  - `wordlist/data/words.txt` (14,855 words) is what a guess may be.
  - A test checks that the answers are sorted, unique, and all in the
    dictionary.
- **Choosing the answer:** the days are split into cycles as long as the answer
  list. Each cycle walks one permutation of the list: a Fisher–Yates shuffle
  seeded with `HMAC(answer key, "cycle:<n>")` through ChaCha8.
  - No answer repeats within a cycle (about six years).
  - The order can't be guessed without the key.
- **Pinning:** the first time a puzzle is needed (a request, or the push at
  07:00), its answer is stored as `puzzle:<n>`. Every later read uses that
  document, so a new secret or an edited answer list never changes a puzzle
  that has already started.
- **Rollover mid-game:** a guess names the puzzle number the page shows. A
  guess for a puzzle that is no longer current is refused with `new_puzzle`,
  and the page reloads the new board.

## Playing

- **Validation:** the server checks every guess and colours it. Two-pass marking
  handles duplicate letters the NYT way. The page never receives a word list,
  and it never receives the answer until that player's game is over.
- **Progress:** each player's board is saved after every guess, per puzzle. A
  restart, a reload, or opening any other card shows the same board.
- **One game a day:** each player gets one game per day across all chats. A
  finished puzzle can't be replayed.
- **Stats:** each player's stats cover every chat. They are played, win %,
  current and max streak, and the guess distribution, shown on the end screen.
  - The current streak counts consecutive puzzle wins.
  - A loss or a missed day ends it.
  - A streak whose last win is older than yesterday shows as 0.
- **End screen:** it shows the answer, the stats, a countdown to the next
  puzzle and two buttons:
  - **Share** uses Telegram's game share.
  - **Copy result** copies the spoiler-free text, such as
    `Wordle Daily #12 3/6` followed by the emoji grid.
- **Page features:** physical and on-screen keyboards, a dark scheme that follows
  the system, and a high-contrast (orange/blue) toggle that is remembered in
  `localStorage`. The board is sized from the height the header and keyboard
  leave, so the page fits a short phone screen without scrolling. Each tile
  turns to its colour in turn, and the keyboard colours once the row is
  revealed. Tiles and keys carry their result as an accessible label, and
  each guess's result is announced to screen readers.
- **Score:** a win reports `7 − guesses` with `setGameScore` (`force=false`). A
  1-guess win scores 6 and a 6-guess win scores 1. A loss is not reported.
  - The score is set on the card the player played from.
  - When they open another card the same day, it is set there too, once per
    card.
  - Each day's card therefore carries that day's leaderboard.
  - A card is recorded as reported only once Telegram accepts the score.
    `BOT_SCORE_NOT_MODIFIED` counts as accepted. Any other failure is logged,
    and the next time the player opens that card the score is set again.

## Groups: mapping the Discord Activity to Telegram

NYT's Discord Activity works like this:

- Everyone plays the same daily word.
- A live embed in the channel shows each player's progress.
- Results are spoiler-free colour grids.
- A daily message recaps yesterday's scores, crowns the best, and shows the
  group streak.
- Reminders of a new puzzle can be muted.

Telegram has different limits:

- A game message has no caption, so any recap has to be a separate text message.
- Bots can edit their own text messages.
- `setGameScore` in a group also posts Telegram's own "X scored N" service
  message, which cannot be avoided.
- A message holds at most 4,096 characters, and groups allow about 20 messages
  a minute.

The bot maps the Activity onto those limits as follows.

| Discord Activity | Here |
|---|---|
| `/wordle` launches the activity | `/wordledaily` sends the card; Play opens the board |
| Live channel embed of everyone's progress | One **live results** message per group, topic and day (below) |
| `/share` and the Share button | **Share** and **Copy result** on the end screen |
| Daily recap of yesterday, crown, group streak | The 07:00 ICT push to subscribed chats (below) |
| Mute daily reminders | `/wordledaily_unsubscribe` |
| Personal streaks | Per-user stats, the same for every chat |

### Live results message

A live results message exists only in groups and supergroups, never in a
private chat or for an inline card. It works like this:

- **Who appears:** a player is listed in a group topic's day once they have
  made a guess from a card in that topic. A player who finished elsewhere and
  then opens the group's card is listed at once, with their finished grid.
- **Creating it:** the first time, the bot sends one silent message as a reply
  to the card, with `AllowSendingWithoutReply`. After that it only edits that
  message.
- **Rate of updates:** edits are coalesced, at most one every 5 seconds per
  message, and the text is always rendered from the store.
- **Errors:**
  - A "message is not modified" answer is ignored.
  - If the message was deleted, a new one is sent at most once every 10
    minutes. An update that comes sooner is held until then, so the final
    results still arrive.
  - A failure that may pass (429, a timeout, a 5xx, a store error) is retried
    up to 5 times, waiting Telegram's `retry_after` or 5, 10, 20, 40 and 80
    seconds. A refused request (bad request, forbidden) is not retried.
- **Content:** colour grids only, never letters. The rows are ordered like
  this:
  1. Winners, by fewer guesses and then earlier finish.
  2. Losses (`X/6`).
  3. Players still playing, with their partial grid, most guesses first.

  The message stops before Telegram's limit and ends with `+N more`.

```
Wordle Daily #12 · live results
🔥 Group streak: 5 days

Alice 3/6
⬜🟨⬜⬜⬜
🟨🟩⬜🟩⬜
🟩🟩🟩🟩🟩
Bob X/6
…
Carol playing 2/6
⬜⬜🟨⬜⬜
⬜🟩🟩⬜⬜
```

### The 07:00 ICT push

The cron `wordledaily_daily_push` (`0 0 * * *` UTC) does the following:

1. Pins today's answer.
2. Deletes the `puzzle:`, `play:` and `cday:` documents of puzzles older than
   yesterday's (see [Storage](#storage)).
3. Claims the day under `daily_push:last_date`, so a second trigger on the same
   day sends nothing.
4. For each subscriber:
   1. In a group, it sends yesterday's recap. The recap opens with yesterday's
      answer, then the group streak, then the players who finished.
   2. It sends today's card in the same topic.

If the recap fails to send, the card is skipped. The shared subscription fan-out
then prunes chats that blocked or removed the bot. Sends are throttled above 30
subscribers.

```
Wordle Daily #11 — yesterday's results
Answer: CRANE
🔥 Your group is on a 5 day streak!
👑 3/6: Alice, Dan
4/6: Eve
X/6: Bob
```

What else the push sends depends on the day before:

- **Group streak:** it counts consecutive puzzles that at least one member of
  the group or topic **solved**; a loss does not keep it going. If nobody
  solved yesterday's word and a streak of at least 2 days was alive, the
  streak line reads `Nobody solved it. Group streak reset.`
- **Nobody played:** the recap is just the title and the answer.
- **Private chats:** a private chat always gets just the card.

**Group streak:** the number of consecutive puzzles on which at least one
player listed in that group topic finished, win or lose. Streak lines appear
only from 2 days up.

Nothing else is posted. There is no separate "X is playing" message and no
message per guess; the live results message covers both.

## HTTP API

Every route lives under `/games/wordledaily/`:

- `GET /games/wordledaily/` serves the page, and `GET app.js` and `GET app.css`
  serve its assets.
- The API routes are `POST` with a JSON body.

Headers, CSP, caching and the 4 KB request limit are the same as noitu's,
through `internal/modules/util/htmlgame`.

| Route | Body | Answer |
|---|---|---|
| `api/state` | `{"token"}` | view |
| `api/guess` | `{"token","num","word"}` | view |

The view has these fields:

- `num`, `date`, `player`, `max` (6), `len` (5)
- `guesses`: a list of `{word, marks}`. `marks` has one letter per position:
  `c` correct, `p` present elsewhere, `w` wrong.
- `status`: `playing`, `won` or `lost`
- `next_at`: the unix second the next puzzle starts
- `answer`, `stats` (`played`, `win_pct`, `cur`, `max`, `dist[6]`) and `share`:
  only once the game is over

Errors are `{"error","message"}`:

| Status | Codes |
|---|---|
| 400 | `bad_request` |
| 401 | `bad_token`, `expired` |
| 409 | `new_puzzle` |
| 422 | `length`, `unknown`, `finished` |
| 429 | `rate_limited`: 60 requests a minute per player, both routes together |

A Play token expires after 6 hours and is not tied to a day.

## Storage

Everything is in the module's collection, under separate key prefixes.

| Key | Holds |
|---|---|
| `puzzle:<n>` | the pinned answer of puzzle `n` |
| `play:<n>:<user>` | one player's board on puzzle `n`: guesses with marks, status, finish time, cards reported on, group topics joined |
| `stats:<user>` | played, wins, current and max streak, guess distribution, last puzzle finished and won |
| `cday:<n>:<chat>:<thread>:` | the players of a group topic's day, the card the summary replies to, and the summary message ID |
| `cstreak:<chat>:<thread>:` | the group topic's streak and the last puzzle it counted |
| `subscribers` | the subscribed chats and topics |
| `daily_push:last_date` | the last puzzle number pushed |

Each player's board and stats are written under a per-player lock. The board
is the record and is written first. A stats update is skipped for a puzzle
already counted, and when the stats write after a finished board fails, the
next state load or guess folds that board into the stats. So the stats always
follow the board and never count a game twice. Group documents use versioned
writes with retries.

**Retention:** the 07:00 push deletes `puzzle:<n>`, `play:<n>:*` and
`cday:<n>:*` for every `n` older than yesterday's puzzle; nothing reads them
after the recap. `stats:`, `cstreak:` and the subscription documents are
kept. With the memory store this keeps the process's memory bounded.

## Choices made

These follow NYT's behaviour where it is known. Change them here if wanted:

- The recap shows yesterday's answer first.
- The group streak counts wins only.
- The live results message shows partial grids of players still playing, as
  NYT's live embed does. This reveals some colour hints to members who have
  not played yet.
- The answer list was curated for this bot and can be replaced. A puzzle that
  has already started keeps its stored answer.
