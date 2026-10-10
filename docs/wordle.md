# Wordle (unlimited)

Unlimited Wordle is the second mode of the `wordledaily` module's HTML5 game.
Each player plays their own rounds, each with a random English five-letter
word, as many as they like. It shares the BotFather game `wordle`, the page
and the word lists with [Wordle Daily](wordledaily.md); only the card decides
which mode Play opens.

## Commands

| Command | What it does |
|---|---|
| `/wordle` | Sends an unlimited card. Play opens the caller's current round. Without the web game (`GAME_BASE_URL` unset) it shows the round's board in the chat instead |
| `/wordle <word>` | Plays the guess on the caller's round and replies with the coloured row, `Guess n/6.`, or the result and streak at the end |
| `/wordle_new` | Gives up the caller's round and starts a fresh one, then sends a new card. Without the web game it confirms in the chat instead |
| `/wordle_giveup` | Ends the caller's round as a loss and reveals the word. On a finished round it only repeats the answer |
| `/wordle_stats` | The caller's unlimited stats: played, wins and win %, current and best streak |

Each player has one round, the same one in every chat: the page and the chat
commands play it together, so a guess typed in the chat shows on the page and
the other way round. In a group every member plays their own round; nobody
shares a board. Channels are refused, and so are anonymous group admins and
posts sent on behalf of a chat: Telegram sends those from one shared stand-in
account in every group, so they would all share one round.

The commands kept their names from the retired in-chat `wordle` module, so
command stats need no migration.

## Rounds

- **Starting:** a player's first round starts the first time they open a card
  or use a command. The word is drawn at random from the answer list
  (`wordlist/data/answers.txt`), never the word of the round it replaces. A
  guess may be any word in `wordlist/data/words.txt`, as in the daily game.
- **Guesses:** six. Marks and validation are the daily game's: the server
  colours every guess and the answer reaches the page only once the round is
  over.
- **Clock:** a round's start time is its first guess, so opening the page
  costs nothing.
- **New game:** the page's **New game** button (in the header and on the end
  panel) and `/wordle_new` give up the round being played and start the next.
  - Giving up a round with at least one guess counts as a loss. The page
    shows the given-up round's word in a toast, as `/wordle_new` does.
  - A round nobody guessed on is just replaced, with no loss. The old in-chat
    `/wordle_new` counted that as a loss too.
  - The header button asks for a second tap ("Give up this round?") when the
    round has guesses.
  - The page sends the round number it shows. A stale number, such as a double
    click, returns the current round instead of starting another.
- **Stats:** played, wins, current streak (consecutive wins) and best streak,
  plus the guess distribution, shown on the end panel. They are separate from
  the daily stats.
- **No high score:** an unlimited win never calls `setGameScore`, so each
  card's high-score table stays the daily leaderboard. There is no share text
  either; the end panel points to `/wordledaily` for the shared word.

## Which card opens which mode

`/wordle` and `/wordle_new` record the card they send as `ucard:<chat>:<message>`
in the module's collection. Play looks the pressed card up:

- A recorded card opens unlimited mode. The token carries `"md":"u"`.
- Anything else opens the daily puzzle: `/wordledaily` cards, the 07:00 push,
  cards sent before unlimited mode existed, inline shares (`?game=wordle`
  links, which have no chat message to record), cards whose record expired,
  and forwarded copies of a `/wordle` card. A forwarded copy is a new message
  with its own chat and message id, and a forward does not say which message
  it copied, so the bot cannot tell an unlimited card from a daily one; it
  plays daily. Send `/wordle` in that chat for your own round.
  Tokens without `md` are daily, so links signed before the change still work.
- If the lookup itself fails, Play answers with an alert instead of silently
  opening the daily puzzle.
- If a card cannot be recorded after it was sent, the bot deletes it and says
  so, because it would otherwise play daily.

A record is refreshed at most once a day when its card is played. The cron
`wordle_unlimited_cards` (`35 20 * * *` UTC, 03:35 ICT) forgets cards nobody
played for 30 days; such a card then plays daily.

## Carrying over the in-chat game

The in-chat `wordle` module was folded into `wordledaily`. At startup,
`wordledaily.InitStore` copies its players from the old `wordle` collection,
once:

- `stats:<user>` becomes the player's unlimited stats (`played`, `wins`,
  `streak` → current, `bestStreak` → best, `lastResultAt`). The old games
  recorded no guess distribution, so it starts at zero.
- An unfinished `game:<user>` becomes the player's current round, with its
  guesses and colours.
- **Group data is not carried.** The old game kept one shared round and one
  set of stats per group chat (a negative id), which belong to no single
  player, so those documents stay where they are, unused.
- Nothing is overwritten: a player who already has unlimited stats or a round
  keeps them.
- The old documents are not changed or deleted, so rolling back is only a
  redeploy of the old binary.
- A completed marker `wordledaily:legacy-wordle-unlimited` in the `system`
  collection makes later boots skip it. It runs whatever `MODULES` says, like
  the stats migration.

A `MODULES` value that still names `wordle` loads `wordledaily` in its place
(with a warning in the log), so a deployment that listed the old module keeps
its `/wordle` commands.

## HTTP API

Unlimited mode uses the daily game's routes under `/games/wordledaily/`; the
token decides the mode. See [Wordle Daily › HTTP API](wordledaily.md#http-api)
for the shared parts.

| Route | Body | Answer |
|---|---|---|
| `api/state` | `{"token"}` | the current round, started if the player has none |
| `api/guess` | `{"token","seq","word"}` | the round after the guess |
| `api/new` | `{"token","seq"}` | the next round; `403 bad_mode` on a daily card |

The unlimited view has `mode: "unlimited"`, `seq` (the round number),
`player`, `max`, `len`, `guesses`, `status` and `gave_up`. `answer` and
`stats` appear only once the round is over. After `api/new` gives up a round
with guesses, `abandoned` holds that round's word; `num`, `date`, `next_at` and
`share` are absent. A guess on another round than the current one answers
`409 new_round`; a guess on a finished round answers `422 finished`.

## Storage

In the `wordledaily` collection:

| Key | Holds |
|---|---|
| `uround:<user>` | the player's current round: number, word, guesses with marks, status, whether they gave up, guess budget, start and finish time |
| `ustats:<user>` | played, wins, current and best streak, distribution, last finish, last round counted |
| `ucard:<chat>:<message>` | an unlimited card and when it was last played |

A round is saved first and is the record; the stats then count it once, by
its round number, and a failed stats write is repaired by the player's next
request. The page and the chat commands take the same per-player lock.

The engine (rounds, cards, tokens and the daily mode) lives in
`internal/modules/util/guessgame` and is shared with [LoLdle](loldle.md).
