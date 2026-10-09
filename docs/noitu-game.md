# Nối từ game

`noitu` is a Telegram HTML5 game: Vietnamese word chaining ("nối từ"). It has
three commands:

- `/noitu` sends the game into a group as a room card: the members who press
  Play on it play each other. See [Playing together](#playing-together-noitu).
  It works only in groups; elsewhere it answers with a hint (see below).
- `/noitubot` sends the game for a solo game against the bot, in a group or a
  private chat.
- `/noitutop` shows the group's leaderboard across its room games. See
  [Leaderboard](#leaderboard-noitutop).

Pressing **Play** opens a page that this bot serves itself. The final score
goes to Telegram's in-chat high-score table.

Until October 2026 the solo game was `/noitu` and the room card was
`/noitupvp`. A one-time startup migration moved their `/stats` history to the
new names: `noitu` to `noitubot` first, then `noitupvp` to `noitu`.

The rules, the dictionary and the bot opponent come from `tiennm99/noitu`. Its
Go code is ported in-tree under `internal/modules/noitu/{dict,engine,opponent}`,
and its dictionary is embedded in the binary.

## Setup

1. **BotFather:** create a game for the bot with `/newgame` and the short name
   `noitu`. The share link is then `t.me/<bot username>?game=noitu`.
2. **Public URL:** attach an HTTPS domain to the bot container's port 8080. In
   Coolify, set the `bot` service's domain to `https://noitu.example.com:8080`.
   The proxy terminates TLS and forwards to the container.
3. **Environment:**
   - `GAME_BASE_URL`: set it to that domain, with no port and no trailing slash,
     for example `https://noitu.example.com`. The page is served at
     `<GAME_BASE_URL>/games/noitu/`.
   - `NOITU_GAME_SECRET` (optional): at least 32 bytes. It signs the game
     links. When it is unset, a key is derived from `TELEGRAM_BOT_TOKEN`, so
     rotating the bot token invalidates game links that are already open.
4. Keep `noitu` in `MODULES`, or leave `MODULES` empty.

With `GAME_BASE_URL` unset or invalid, the game is disabled:

- `/noitu` and `/noitubot` still send the game.
- Pressing Play shows the alert "Trò chơi nối từ chưa được cấu hình trên máy
  chủ này."
- No `/games/` route exists, so the bot needs no public ingress.

A `GAME_BASE_URL` that is not `https://host[/path]` is logged as a warning and
also disables the game. So is a `NOITU_GAME_SECRET` shorter than 32 bytes.

## How a game flows

1. `/noitubot` calls `sendGame` with no keyboard, so Telegram adds the Play
   button itself. In a forum topic the game stays in the topic. In a channel the
   bot answers with a short text instead, because Telegram does not allow games
   in channels.
2. Play delivers a callback query with `game_short_name=noitu`. The module
   answers it with a URL: `<GAME_BASE_URL>/games/noitu/?t=<token>`. The token
   is signed, expires after 6 hours, and names the player and the game message.
   The game message is either a chat message (`chat_id` + `message_id`) or, for
   a game sent through a `?game=` share, an `inline_message_id`.
3. The page sends the token to `api/start` and gets back a session. From then
   on the session ID is what identifies the game.
4. Each word goes to `api/move`. The server validates it, plays the bot's reply
   in the same request, and returns the new state.
5. When the game ends, the server reports the score with `setGameScore`
   (`force=false`), once, and only if the score is above 0.

The client never sends a score. The server owns the chain, the clock and the
points.

## Rules

- **Opening:** the bot opens with a random word whose last syllable starts at
  least 20 words. The player moves first.
- **Valid word:** at least 2 syllables, present in the dictionary, starting with
  the previous word's last syllable, and not already played in this game.
- **Order of checks:** syllables, dictionary, link, reuse. A rejected word keeps
  the turn and the clock.
- **Accepted spellings:**
  - Input is normalized: Unicode NFC, lowercase, and collapsed spaces.
  - Alternative tone placement in `oa`/`oe`/`uy` is accepted (`hoà` and `hòa`).
  - An `i`/`y` swap after `h k l m t qu` is accepted, as are the `sĩ`/`sỹ` and
    `vĩ`/`vỹ` pairs.
  - A variant counts only when exactly one dictionary word matches it.
- **Turn timer:**
  - Each turn lasts 30 seconds.
  - A move that arrives within 2 more seconds still counts, which absorbs
    network latency, but it earns no speed points.
  - After that the player loses on time.
  - A player handed a dead end keeps the turn. When the clock runs out the
    game ends as `no_legal_move`, not as a timeout. Pressing "Chịu thua" in a
    dead end ends it as `no_legal_move` at once; in a playable position it
    ends as `gave_up`.
- **Bot:**
  - The player picks the bot's difficulty: easy (a random legal word), medium
    (one-move lookahead; the default), or hard (a 4-ply negamax, capped at
    20,000 nodes).
  - The bot answers instantly, and the page shows a short pause.
  - If the bot has no legal word, the player wins.
- **Chain cap:** a chain that reaches 300 words also ends as a player win.
- **After a loss:** the page shows up to 3 words the player could have played.
  An empty list means the position was a true dead end.

## Scoring

Each of the player's accepted words scores the sum of five parts, capped at 100
points per word:

| Part | Points |
|---|---|
| Base | 10 |
| Chain | 2 × min(words already played, 15) |
| Length | 5 × (syllables − 2) |
| Speed | 10 × time left ÷ 30 s (measured on the server) |
| Rarity | max(15 − 3 × ⌊log₂(words that start with the answered syllable)⌋, 0) |

The game score is the sum of the player's words. The bot's words are shown but
do not count.

`setGameScore` uses `force=false`, so Telegram keeps each player's best. A
score that is not higher returns `BOT_SCORE_NOT_MODIFIED`, which the page
treats as saved.

## HTTP API

Every route lives under `/games/noitu/`:

- `GET /games/noitu/` serves the page, and `GET /games/noitu/app.js` and
  `GET /games/noitu/app.css` serve its assets.
- Every API route is a `POST` with a JSON body.

| Route | Body | Answer |
|---|---|---|
| `api/start` | `{"token","difficulty":"easy\|medium\|hard"}` | session view |
| `api/move` | `{"session","word"}` | `{"result":{accepted,reason,message,player_word,bot_word},"state":view}` |
| `api/state` | `{"session"}` | session view; also settles an expired turn |
| `api/give-up` | `{"session"}` | session view; `no_legal_move` in a dead end, otherwise `gave_up` |

The session view has these fields:

- `session`, `player`, `difficulty`
- `status`: `playing`, `won` or `lost`
- `end_reason`: `timeout`, `no_legal_move`, `gave_up`, `bot_stuck` or
  `max_moves`
- `current`: the syllable the next word must start with
- `chain`: a list of `{word, by: bot|player, points, meanings[{pos, gloss}]}`
- `score`
- `turn_limit_ms`, `deadline_ms` and `server_now_ms`: the page counts down
  from `deadline_ms − server_now_ms`, so it never depends on the phone's clock
- `suggestions`
- `score_reported`: `pending`, `ok`, `failed` or `skipped`

A move the rules reject answers `accepted:false` with one of these reasons:
`too_few_syllables`, `not_in_dictionary`, `wrong_link`, `already_used` or
`timeout`. Each comes with a Vietnamese message.

Errors are `{"error","message"}`:

| Code | Status |
|---|---|
| `bad_request` | 400 |
| `bad_token` | 401 |
| `token_expired` | 401 |
| `no_session` | 404 |
| `game_over` | 409 |
| `too_fast` | 429 |
| `busy` | 503 |

## Limits and security

- **Token:**
  - Format: `base64url(json) "." base64url(HMAC-SHA256)`, checked with
    `hmac.Equal`.
  - It expires after 6 hours and is checked only at `api/start` and
    `api/room/join`.
  - It travels in the query string, never in the path. The request log records
    only the path. The page removes it from the address bar on load and keeps
    it in memory and `sessionStorage`, so copying or sharing the page link
    does not leak it.
  - Every response sends `Referrer-Policy: no-referrer`, so the token cannot
    leak through the referrer.
- **Request limits:**
  - Bodies are capped at 4 KiB, and unknown JSON fields are rejected.
  - A word is at most 64 characters and 8 syllables.
  - At most one move per 300 ms per session.
  - At most 10 starts per user per minute.
- **Sessions:**
  - At most 2,000 live sessions; beyond that, `api/start` answers `busy`.
  - At most 3 live sessions per user. A fourth start ends the user's oldest
    session.
  - A new start replaces the player's own session on the same game message.
  - A session that a start replaces or ends is settled first: an unfinished
    game ends as given up (or `no_legal_move` in a dead end), and a positive
    score is still reported.
  - Sessions live in memory only, so a restart ends live games. The page then
    returns to the start screen with "Ván chơi không còn nữa. Hãy bắt đầu ván
    mới."
  - An idle game is dropped after 15 minutes, and a finished one after 2
    minutes.
  - A minutely cron settles turns that ran out with the page closed, so those
    scores are still reported.
- **Headers:**
  - A strict CSP: the page has no inline script or style, and only
    `https://telegram.org` is allowed as an external script source.
  - `X-Content-Type-Options: nosniff`.
  - API responses are `no-store`.
  - Framing is not restricted, because Telegram Web shows games in an iframe.

## Playing together (`/noitu`)

`/noitu` works in groups and supergroups only. In a private chat or a channel
it does not send a game; it answers "Gửi /noitu trong nhóm để chơi cùng nhau.
Muốn chơi với bot thì dùng /noitubot nhé." It sends the same
BotFather game, `noitu`, and records the sent message as a **card** in the
module's storage collection (`noitu`, key `pvp:<chat_id>:<message_id>`, with
the forum topic). If the card cannot be recorded, the bot deletes it again and
answers "Không tạo được phòng nối từ. Thử lại sau nhé."

- Play on a card issues a token marked PvP (`p`), carrying the card's forum
  topic (`t`). The page reads that flag and opens the room instead of the
  start screen. The server checks the flag again: `api/start` refuses a PvP
  token and `api/room/join` refuses any other.
- A forwarded copy of a card is a different message, and a `?game=` share is
  an inline message; neither is a card, so both play against the bot.
- A card stays a room for 30 days after its last Play press. Play refreshes
  that date at most once a day, and a daily cron (`noitu_pvp_cards`, 03:30
  ICT) forgets older cards, which then play solo. The cron runs whenever the
  storage is available, even while the game is disabled.

### Rooms

- Everyone who presses Play on the same card joins **one room**. A second
  press, or a reload, keeps the member's place.
- The lobby lists the members in join order. The first is the **host** and
  starts a game with "Bắt đầu", which needs at least 2 players.
- A game seats the first 8 members, in join order. Anyone else, and anyone
  who joins during a game, watches.
- When a game ends, the room returns to the lobby with the result, and the
  host can start the next game on the same card.
- Rooms live in memory only, so a restart ends them. A lobby member whose page
  stops polling for a minute leaves the room, and the next member becomes
  host; a running game never drops anyone. A room without a game is dropped
  once nobody is left, so a lobby stays open while any page still polls. A
  page whose room is gone joins again with its token, which opens a fresh
  room on the same card.
- Limits: at most 500 live rooms, and at most 3 per chat. Joins share the solo
  limit of 10 starts per user per minute. The host's "Bắt đầu" counts against
  that same limit, and one room starts at most one game every 3 seconds.

### Room rules

The words, the checks, the 30-second turn with its 2-second grace and the
per-word points are the same as against the bot (see [Rules](#rules) and
[Scoring](#scoring)); one engine implements both. What changes:

- Turns rotate in join order, starting with the first seat. The bot only plays
  the opening word.
- A player whose turn runs out, or who presses "Chịu thua", is **eliminated**.
  The syllable and the used words stay as they were, and the turn passes to
  the next player still in.
- "Chịu thua" also works out of turn, to leave a game. In a dead end on the
  player's own turn it counts as no legal move.
- A word handed to the next player as a dead end does not end anything: that
  player loses it on the clock or with "Chịu thua", and everyone behind them,
  who faces the same board, goes out with them. The player who closed the
  position wins.
- An expired turn is settled on the next request or by the minutely sweep.
  The next turn starts when the expired one ended, grace included, so a late
  settlement does not shorten or lengthen anyone's turn.
- The **last player standing** wins. If the chain reaches 300 words, the
  player still in with the most points wins.

### Room scoring

- Each player scores the per-word points of their accepted words. The winner
  gets 50 more.
- A game in which nobody played a word (everyone left at once) earns nothing:
  no bonus, no score report, no announcement and no leaderboard entry.
- When a game ends, the bot posts "Ván nối từ kết thúc: <tên> thắng sau <n>
  từ!" as a reply to the card, in its topic. A failure is logged and ignored.
- Then it reports each player whose score is above 0 with `setGameScore`
  (`force=false`) on the card, one at a time. Results of one card are
  published one game after another, so two games never edit the card at
  once; results of different cards publish independently. The card's
  high-score table therefore ranks the group, and keeps each member's best.

### Room API

All room routes are `POST` with a JSON body, under `/games/noitu/api/room/`.
The join answers a `member` bearer; every other route takes it.

| Route | Body | Answer |
|---|---|---|
| `join` | `{"token"}` | room view |
| `state` | `{"member","version"}` | room view, or `{"version","unchanged":true,"server_now_ms"}` when `version` is current |
| `start` | `{"member"}` | room view; host only |
| `move` | `{"member","word"}` | `{"result":{accepted,reason,message,player_word},"state":view}` |
| `give-up` | `{"member"}` | room view |

The page polls `state` every second (every 4 seconds while hidden) and sends
the last `version` it saw, so an unchanged room costs one small answer. Every
change to the room, a finished score report included, bumps the version.

The room view has these fields:

- `member`, `version`, `game` (games started in this room)
- `status`: `lobby` or `playing`
- `host`: whether this member hosts the lobby
- `seat`: this member's seat in the running game, `-1` otherwise; `turn`: the
  seat to act, `-1` in the lobby
- `players`: `{name, you, host, score, alive, out_reason}`, the seats in a
  game or the first 8 members in the lobby; `watchers`: everyone else
- `min_players`, `max_players`
- `current`, `chain` (entries also carry `seat`, `-1` for the opening, and
  `name`), `turn_limit_ms`, `deadline_ms`, `server_now_ms`
- `result`: the last finished game until the next starts, `{winner, words,
  end_reason, standings[{name, rank, score, bonus, words, out_reason, you}],
  score_reported}`

A word sent out of turn, by an eliminated player or by a watcher answers
`accepted:false` with reason `not_your_turn`; a word that arrives after the
grace period answers `timeout`.

| Code | Status |
|---|---|
| `no_room` | 404: unknown member, or the room is gone |
| `not_host` | 403 |
| `room_full` | 409: 32 members |
| `not_enough_players` | 409 |
| `game_running` | 409 |
| `no_game` | 409: move or give-up without a running game |
| `not_in_game` | 409: give-up by a watcher or an eliminated player |

The solo limits apply to rooms too: the 4 KiB body cap and strict JSON, the
word length, one move per 300 ms per member, and the 10 starts per user per
minute, which joins share. At most 500 rooms exist; beyond that a join answers
`busy`. Player names are Telegram first names cut to 64 characters; the page
only ever renders them as text.

## Leaderboard (`/noitutop`)

Every room game in which at least one word was played (the games that are
announced) updates the chat's leaderboard. It is stored in the module's
storage collection (`noitu`), one document per player and chat, keyed
`top:<chat_id>:<user_id>`:

- games played and games won, as a seated player
- the best single-game score and the total of all game scores, winner bonus
  included
- the player's latest first name

The update runs in the background publisher after the room is back in the
lobby, before the announcement. A failure is logged and skips only that
player. Two games ending at once for the same player are both kept: each
update is a versioned write that retries on conflict.

`/noitutop` in a group lists the top 10, ranked by wins, then the best game,
then fewer games played (the same record in fewer games ranks higher), then
total points. A caller ranked below 10 also sees their own line under the
table. Names are HTML-escaped. In a private chat or a channel it answers
"Bảng xếp hạng nối từ chỉ có trong nhóm…". Telegram's own high-score table on
each card is separate: it keeps each member's best score on that card only.

## Sharing, and why there is no inline mode

Players share a game in three ways:

- by forwarding the game message;
- with the `t.me/<bot>?game=noitu` link;
- with the share button Telegram shows on the game page.

When `telegram.org/js/games.js` loads, the page also shows "Chia sẻ điểm",
which calls `TelegramGameProxy.shareScore()`. The page works without that
script.

Inline-mode game results (`@bot noitu`) are not offered. Inline queries have a
single owner in the registry, the `alias` module, which answers them with saved
aliases. Mixing game results into that handler would couple two unrelated
modules for a sharing path the three above already cover.

A game sent through a `?game=` link is expected to come back as an inline
message (`inline_message_id`). Its score is reported with a raw Bot API call,
`internal/telegram/game_score.go`, because `go-telegram/bot` v1.20.0 types
`inline_message_id` as an int and cannot decode Telegram's `true` answer.

## Attribution

The dictionary is
[`internal/modules/noitu/dict/data/dictionary.txt`](../internal/modules/noitu/dict/data/dictionary.txt).
It is derived from [Wiktionary tiếng Việt](https://vi.wiktionary.org/) and
licensed under [CC BY-SA 4.0](../internal/modules/noitu/dict/data/LICENSE).

- It is embedded unmodified with `go:embed`.
- Its [ATTRIBUTION.md](../internal/modules/noitu/dict/data/ATTRIBUTION.md)
  records the source and the modifications.
- The page footer credits it.
- CC BY-SA applies to the data only. The bot's code stays Apache-2.0.
