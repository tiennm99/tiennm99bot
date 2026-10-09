# Aliases

The `alias` module lets anyone give a short name to a message and send it back
later by that name.

| Command | Parameters | Reply to | What it does |
|---|---|---|---|
| `/alias` | `<name>` | any supported message | Saves it under that name |
| `/insert` | `<name>` | — | Sends back whatever is saved under it |
| `/aliases` | — | — | Lists every saved name |
| `/unalias` | `<name>` | — | Deletes a saved name |
| `/<name>` | — | — | Same as `/insert <name>` |
| `@botname <prefix>` | — | — | Inline picker, in any chat |

All are public and single-shot.

## The namespace is global

A name assigned in any chat works in **every** chat, for **everyone** — the same
way the sticker pack `/addsticker` writes to is shared. The store is a plain map
from name to content, with no chat or user in the key.

The consequence is worth stating plainly: anyone can reassign anyone's name.
`/alias` overwrites rather than refusing, and its reply says what it replaced —

> Replaced /insert cheer — it was a sticker, now it is a GIF.

Overwriting is deliberate: refusing would make a mistyped alias awkward to
correct. `/unalias <name>` deletes one, and is open to anyone for the same
reason overwriting is — the namespace is shared, so the permission model is too.
A per-owner restriction would leave an alias whose assigner has left the chat
permanently unremovable.

## Invoking an alias

Three ways, same binding:

1. **`/<name>`** — a saved name works as its own command. `/cheer` is `/insert cheer`.
2. **`/insert <name>`** — always works, including when inline mode is off.
3. **`@botname <prefix>`** — an inline picker with previews, usable in any chat,
   even ones the bot is not a member of.

**Code always beats an alias.** The dispatcher registers every real command
before the alias fallback, and the bot library returns the *first* handler whose
matcher accepts an update — so a command defined in code can never be shadowed
by a name resolved at runtime. `/alias` refuses a name that is already a
command for the same reason, since such an alias would only ever be reachable
through `/insert`.

If a future build adds a command whose name an alias already uses, the command
silently wins and the alias stays reachable via `/insert`. That is the intended
precedence, not a bug to fix.

**An unknown `/command` is answered with silence.** The fallback sees every
unrecognised command in every chat the bot is in, so replying would turn a typo
like `/pign` into noise, and would confirm to anyone probing which names exist.

## Inline mode

`@botname` with no query lists everything; with a query it filters by name
prefix, case-insensitively, sorted, capped at Telegram's 50 results per answer.

Each result is a **cached** inline type — it carries the `file_id` Telegram
already holds, so nothing is uploaded and the picker shows real previews. This
is the payoff for storing a `file_id` rather than bytes.

**Video-note aliases do not appear inline.** Telegram defines no
`InlineQueryResultCachedVideoNote`, and substituting a plain video would change
what was saved. They stay reachable through `/insert` and `/<name>`. The 50-cap
counts results the picker can show, so a skipped kind does not eat a slot.

**Answering is a race, and losing it is silent.** Telegram expires an inline
query and then rejects the answer with

> Bad Request: query is too old and response timeout expired or query ID is
> invalid

Every keystroke opens a *new* query, and the bot dispatches updates one at a
time, so one slow answer also delays the queries queued behind it — each ageing
while it waits. One slow read can therefore expire a whole burst of typing.
Two rules keep that from happening:

- The handler reads the store **once** per query (`Scan`), never a name listing
  followed by a read per name. An N+1 read costs a round trip per saved alias,
  on every keystroke.
- It runs under a 3-second deadline, not the 10 seconds the commands get. An
  answer that late is rejected anyway; abandoning it frees the worker for the
  fresher query behind it.

When the rejection does appear, the log line carries how long the answer took —
`answer 4 results after 12.4s: ...` — which separates a slow handler from a
query that was already stale on arrival.

**Two things gate inline mode, and both fail silently.**

1. `inline_query` must be in `pollingAllowedUpdates`
   (`internal/telegram/client.go`). Telegram filters getUpdates server-side, so
   a missing kind means the handler is never called — no log line, no error.
2. Inline mode must be enabled for the bot in BotFather (`/setinline`). Until
   it is, Telegram does not offer the bot for inline use at all, so typing
   `@botname` shows nothing. This is a one-time operational step that cannot be
   done from code.

## Names

One word, username-shaped: starts with a letter, then letters, digits and
underscores, up to 32 characters. A leading `@` is stripped rather than
rejected, since these names imitate usernames and typing the sigil is a natural
slip.

Lookups fold case — `/insert LOUD` and `/insert loud` find the same entry — and
the spelling the assigner used is what gets echoed back.

Telegram's own username minimum is 5 characters; this allows 1 on purpose. The
point of an alias is to be shorter than what it replaces, and `gg` is a good
name for a sticker.

## What can be saved

| Replied message | Sent back with |
|---|---|
| Sticker | `sendSticker` |
| Photo | `sendPhoto` |
| GIF / animation | `sendAnimation` |
| Video | `sendVideo` |
| Video note | `sendVideoNote` |
| Audio | `sendAudio` |
| Voice message | `sendVoice` |
| File / document | `sendDocument` |
| Plain text | `sendMessage` |

Anything else — a location, a poll, a contact — is refused with the list above.

**Nothing is downloaded.** Every media kind is kept as the `file_id` Telegram
already issued, and `/insert` hands that same id straight back to a send call.
The module stores bytes for nothing but the name and a caption. A `file_id`
refers to a file on Telegram's servers, so an alias survives restarts and
redeploys.

The kind is stored alongside the id because a bare `file_id` does not say which
send method will accept it.

**Order matters when Telegram fills more than one field.** A GIF arrives as an
`Animation` *and* a `Document`, and the more specific kind is claimed first —
otherwise `/insert` would hand back a plain file instead of a looping GIF.

**Captions come back too**, for the kinds that can carry one. Stickers and video
notes cannot, and Telegram's send methods for them have no caption field at all.

## Listing

`/aliases` prints the count and every name, sorted, in one message — one line
per alias, showing what the name holds, with the invocation in a `<code>` span
so tapping it copies a command ready to send:

```
3 aliases:
/cheer — sticker
/clip — video
/greeting — text
```

Names list in their folded (lowercase) form, which is exactly what `/insert`
takes.

This is one store read total — `DocStore.Scan` returns the names with their
documents, and the kind that labels each line lives in the document. The list is
trimmed to Telegram's 4096-character limit and ends with `…and N more.`; the
count at the top is always the true total.

## Behaviour worth knowing

**Formatting survives.** Bold, italic, code, links and mentions are stored as
entities alongside the text, and captions keep theirs too. This works because
the text is re-sent byte-identical: entity offsets are relative to that text,
so they stay valid. They are sent back as entities rather than re-rendered as
markup, which avoids escaping and re-parsing content the user never wrote as
markup.

**Another bot's message needs group setup.** By default Telegram strips another
bot's message out of a reply, so there is nothing to store. It arrives intact
once the bot has Bot-to-Bot Communication Mode enabled and is a group admin; see
[Group setup](deploy-coolify-selfhosted.md#4-group-setup).

Every refusal for a message that could not be *read* — as opposed to one whose
kind is unsupported — ends with the same advice, because it works without any
setup: **forward it into the chat and reply to your copy.** A forwarded
copy is a new message sent by a user, so it arrives intact. Three shapes reach
that advice, and they are told apart deliberately:

| What arrived | Answer |
| --- | --- |
| Reply from a sender marked as a bot | Telegram hid that bot's message; admin rights plus Bot-to-Bot Communication fix it |
| Reply with a message id but no content field at all | That message reached me with no content |
| No reply attached at all | Reply to the message you want to save — and if you did, Telegram did not pass it along |

The middle case exists because the sender is not always marked: an anonymous or
service-posted message can arrive equally empty. None of the three lists the
supported kinds, which would blame the format of a message the bot was never
shown — it may well have been a photo. Only a reply that *did* arrive with
content of a kind the module refuses (a poll, a location) gets that list.

**A `file_id` can stop working** — the original file was deleted, or Telegram
rejects it. `/insert` answers with something actionable rather than a generic
failure:

> "gone" can no longer be sent. Save it again with /alias gone.

**Replies keep their forum topic.** Every send forwards `MessageThreadID`, for
the reason `chathelper.Reply` documents: without it Telegram routes the message
to a supergroup's General topic instead of the topic the command was typed in.

## Debugging a capture

Set `LOG_LEVEL=debug` and every `/alias` logs one `alias_capture` line
describing what Telegram actually delivered:

```
alias_capture reply=present reply_id=9 fields=none text_len=0 caption_len=0
              entities=0 captured=false from_id=555 from_bot=true
```

`fields=none captured=false from_bot=true` is the signature of another bot's
message arriving stripped. `reply=absent` means Telegram delivered the command
with no reply attached at all — indistinguishable from the caller forgetting to
reply, which is why the line exists.

It reports **shape, never content**: field names, lengths and counts, but no
message text. The line lands in stdout and whatever ships it, so aliased
messages must not travel with it; a test asserts nothing leaks.

Both command handlers run under a 10-second deadline; the inline handler under
3. The bot processes updates one at a time, so those bounds are what keep a slow
store from stalling other users.
