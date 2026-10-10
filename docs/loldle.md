# LoLdle

`loldle` is a League of Legends "guess the champion" game, after loldle.net's
classic mode. Each guess is scored on seven attributes of the champion, and
the colours say how close it is. It runs as a Telegram HTML5 game behind the
one BotFather game `loldle`, in two modes:

- **Unlimited:** each player has their own rounds, each with a random
  champion. `/loldle` sends the card; the page's **New game** starts the next
  round. The chat commands play the same rounds.
- **LoLdle Daily:** one shared champion a day for everyone, new at 07:00 ICT,
  like [Wordle Daily](wordledaily.md): live group results, a 07:00 recap and
  push, and a high score on the card.

The two modes share the engine in `internal/modules/util/guessgame` with
[Wordle](wordle.md) and [Wordle Daily](wordledaily.md).

## Commands

| Command | What it does |
|---|---|
| `/loldle` | Sends an unlimited card. Without the web game it shows the caller's board in the chat instead |
| `/loldle <champion>` | Plays the guess on the caller's round: the attribute rows, then `Guess n/max.`, or a sticker, a flavour line, the time and the streak at the end |
| `/loldle_giveup` | Ends the caller's round as a loss, with the give-up sticker, and reveals the champion |
| `/loldle_stats` | The caller's unlimited stats |
| `/loldle_setmax <1-10>` | Owner only. Sets the length of unlimited rounds started in this chat from now on |
| `/loldledaily` | Sends today's LoLdle Daily card |
| `/loldledaily_subscribe` | Opts the chat or forum topic into the 07:00 ICT push |
| `/loldledaily_unsubscribe` | Opts it out |

Champion names are matched loosely: case, spaces and punctuation do not
matter (`kaisa` is Kai'Sa), and a unique prefix is enough (`aat` is Aatrox).
An ambiguous prefix, an unknown name and a champion already guessed this
round are refused without using a guess.

Each player has one unlimited round, the same in every chat, and the page and
the chat commands play it together. In a group every member plays their own
round. When a round is over, the next `/loldle` or `/loldle <champion>` starts
a new one, as the in-chat game always did. Channels are refused, and so are
anonymous group admins and posts sent on behalf of a chat, as for
[Wordle](wordle.md#commands).

## Setup

1. **BotFather:** create a game with `/newgame` and the short name `loldle`.
2. **Public URL:** the same `GAME_BASE_URL` as the other games. The page is
   served at `<GAME_BASE_URL>/games/loldle/`.
3. **Environment:** `LOLDLE_GAME_SECRET` is optional and must be at least 32
   bytes. It derives the link-signing key (`tiennm99bot/loldle/token/v1`) and
   the daily answer key (`tiennm99bot/loldle/answer/v1`). Unset, the bot token
   is the root key.
4. Keep `loldle` in `MODULES`, or leave `MODULES` empty.

Without `GAME_BASE_URL`, a long enough secret or storage, the web game is
disabled: `/loldle` and its helpers still play in the chat, `/loldledaily`
and `/loldledaily_subscribe` answer "LoLdle isn't configured on this server.",
Play shows the same alert, and no route or cron exists.
`/loldledaily_unsubscribe` always works.

## The comparison

| Column | Compared | Colours |
|---|---|---|
| Gender | exact | green or red |
| Species | set | green when equal, yellow when they overlap, red otherwise |
| Range type | set | as above |
| Resource | exact | green or red |
| Region(s) | set | as above |
| Position(s) | set | as above |
| Release year | year | green when equal; red with ↑ when the answer is newer, ↓ when older |

The server stores each guess as the champion's name and seven marks in that
order: `c` correct, `p` partial, `w` wrong, and for the year `u` (answer
newer) or `d` (answer older). The page and the chat boards rebuild the cells
from the marks and the guessed champion's own values, so the answer's values
never leave the server.

## The page

Plain HTML, CSS and JavaScript embedded in the binary, no build step, under
the same strict CSP as the other games plus one image origin,
`https://ddragon.leagueoflegends.com`, for champion icons. It is dark,
mobile-first and works in Telegram's in-app browsers.

- **Header:** "LoLdle", the mode (`Daily #N · date` or `Unlimited · Round N`),
  the player, and a guess counter.
- **Search:** an ARIA combobox. Typing lists up to 8 champions, those whose
  name starts with the text first, then those that contain it, without the
  ones already guessed; matching ignores case, spaces and punctuation. Arrow
  keys move, Enter or a tap guesses, Escape or a tap outside closes. The list
  comes from `champions.json`: names and icon ids, nothing else. If it fails
  to load, the page retries with backoff (1 s doubling to 30 s, and at once
  on the next keystroke), says so on Enter, and searches the open query
  again when it arrives.
- **Board:** a Champion column with icon and name, then the seven columns,
  newest guess first. On a narrow screen it scrolls sideways with the
  Champion column pinned, and cells shrink at 768 and 480 pixels. A new row's
  cells appear one after another (80 ms apart), except under
  `prefers-reduced-motion`.
- **Icons:** `https://ddragon.leagueoflegends.com/cdn/img/champion/tiles/<id>_0.jpg`.
  An icon that fails to load is replaced by the champion's initial.
- **Screen readers:** each cell is labelled with its column, value, result
  and, for the year, which way the answer lies; a new guess is announced the
  same way.
- **End panel:** "You got it!" with the guess count, or "Game Over" with the
  champion's icon, name and title; then the stats and guess distribution.
  Daily adds a countdown to 07:00 ICT, **Share** and **Copy result**;
  unlimited adds **New game** and a pointer to `/loldledaily`.
- **New game** in the header asks for a second tap when the round has
  guesses.

## Unlimited rounds

- **Length:** a round allows 8 guesses unless the chat it starts in has a
  `/loldle_setmax` value (1–10). A round keeps the length it started with.
  - From a command, the chat is the command's chat (a group, or the private
    chat).
  - From the page (its first round, or **New game**), the chat is the card's.
    An inline card uses 8.
- **New game** and an abandoned round follow [unlimited Wordle](wordle.md#rounds):
  giving up a round with a guess counts as a loss and the page shows its
  champion, an unguessed round is replaced, and a stale round number starts
  nothing.
- **Stats:** played, wins, current streak (consecutive wins), best streak and
  the guess distribution (up to 10).
- **No high score:** unlimited wins are not reported with `setGameScore`; a
  round's length can differ by chat, so the scores would not compare.
- **Data refresh:** a round whose champion was removed from the data is
  replaced without counting.

Which card opens which mode works as for Wordle: `/loldle` records its cards
(`ucard:<chat>:<message>`, refreshed once a day when played), and every other
card, `/loldledaily`, the push, an inline share, a card unplayed for 30 days or
a forwarded copy of a `/loldle` card, opens the daily champion. The cron `loldle_unlimited_cards` (`40 20 * * *`
UTC) forgets old records. See [unlimited Wordle](wordle.md#which-card-opens-which-mode).

## LoLdle Daily

Everything works as in [Wordle Daily](wordledaily.md), with these values:

- **Puzzle numbering:** puzzle #1 is 9 October 2026, the same epoch as Wordle
  Daily, so both games share their day number.
- **Answers:** the champions sorted by name, walked in a keyed permutation per
  cycle. The first resolution of a day pins its champion as `puzzle:<n>`, so
  adding champions or changing the secret only affects days not started yet.
  If a data refresh removes a champion that is already pinned, that day stays
  playable: its attributes are unknown, so every guess scores 🟥 in every
  column and the board ends in a loss that reveals the name (naming the
  champion still wins, but the page's search no longer lists it).
- **Guesses:** 8. A win scores `9 − guesses` with `setGameScore`.
- **Live results:** one message per group topic and day, with one row of 7
  squares per guess (🟩 correct, 🟨 partial, 🟥 wrong). No champion is named.
- **Push:** the cron `loldledaily_daily_push` (`0 0 * * *` UTC). The recap
  opens with yesterday's champion, then the group streak (wins only), then the
  finishers grouped by guess count:

```
LoLdle Daily #11 — yesterday's results
Champion: Ahri
🔥 Your group is on a 3 day streak!
👑 2/8: Alice, Dan
5/8: Eve
X/8: Bob
```

- **Share text:** `LoLdle Daily #11 2/8` and the square rows.

## HTTP API

Routes under `/games/loldle/`:

| Route | Body | Answer |
|---|---|---|
| `GET /`, `app.js`, `app.css` | | the page |
| `GET champions.json` | | `[{"name","id"}]`, cached for an hour |
| `POST api/state` | `{"token"}` | view |
| `POST api/guess` | `{"token","num"}` (daily) or `{"token","seq"}` (unlimited), plus `"name"` | view |
| `POST api/new` | `{"token","seq"}` | the next unlimited round; `403 bad_mode` on a daily card |

The view has `mode`, `player`, `max`, `columns` (`{key, label}` ×7),
`guesses` and `status`; daily adds `num`, `date` and `next_at`, unlimited adds
`seq` and `gave_up`, and after `api/new` gives up a round with guesses,
`abandoned` (`{name, id, title}`) names its champion. Each guess is `{name, id, marks, cells}`, a cell being
`{key, value, result, dir}` with the guessed champion's own value. `answer`
(`{name, id, title}`), `stats` and the daily `share` appear only once the game
is over.

Errors are `{"error","message"}`: `400 bad_request`, `401 bad_token` /
`expired`, `403 bad_mode`, `409 new_puzzle` / `new_round`, `422 unknown` /
`ambiguous` / `duplicate` / `finished`, `429 rate_limited` (60 requests a
minute per player).

## Storage

All in the `loldle` collection:

| Key | Holds |
|---|---|
| `config:<chat>` | the chat's `/loldle_setmax` value |
| `uround:<user>`, `ustats:<user>`, `ucard:<chat>:<message>` | unlimited rounds, stats and cards |
| `puzzle:<n>`, `play:<n>:<user>`, `dstats:<user>`, `cday:…`, `cstreak:…`, `subscribers`, `daily_push:last_date` | LoLdle Daily, as Wordle Daily's keys; the stats prefix is `dstats:` because `stats:` holds the in-chat game's old stats |
| `game:<subject>`, `stats:<subject>` | the in-chat game before unlimited mode; read once by the migration, never written |

## Carrying over the in-chat game

At startup `loldle.InitStore` copies the in-chat game's players into
unlimited mode, once (marker `loldle:legacy-unlimited` in the `system`
collection), whatever `MODULES` says:

- `stats:<user>` becomes the player's unlimited stats (`streak` → current,
  `bestStreak` → best); the distribution starts at zero.
- `game:<user>`, a round still being played (the old game deleted finished
  ones), becomes the player's current round, with its frozen length and start
  time. Its colours are rebuilt from today's champion data; a guess naming a
  champion no longer in the data is dropped, and a round whose answer is gone
  is not carried.
- Group rounds and group stats (negative subjects) belong to no single player
  and are not carried.
- Nothing already there is overwritten, and the old documents stay, so a
  rollback is a redeploy.

## Champion data

`internal/modules/loldle/data/champions.json` holds 172 champions in
loldle.net's schema (gender, positions, species, resource, range type,
regions, release date). `id` (the Data Dragon key, which names the icon, e.g.
`MonkeyKing` for Wukong) and `title` were added by name from
`tiennm99/loldle`'s Data Dragon scrape. Data Dragon's tile files are
case-sensitive and one does not match its key: Fiddlesticks' tile is
`FiddleSticks_0.jpg`, so `tileIDs` in `champions.go` overrides that `id` at
load. Tests check that every champion has a unique, letters-only `id` and a
title, and that the override applies.

Locke, in that scrape but not here, is not added yet: this data needs his
species, positions and release date, which the scrape does not have.
