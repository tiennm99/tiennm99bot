# Sticker pack

`/addsticker` appends a sticker to **one shared pack** that every user of the
bot contributes to. It is the whole of the `sticker` module — one command, no
storage, and no per-user packs.

**The module stores nothing.** Its factory ignores the collection it is handed:
the pack comes from the environment and the set owner from `OWNER_ID`, so there
is nothing per-user to key.

| Command | Parameters | Reply to | What it does |
|---|---|---|---|
| `/addsticker` | `[emoji...]` | sticker, photo, image document, video, GIF, or video note | Adds it to the shared pack and replies with the link |

Single-shot: one message replying to the media to add. No conversation state.
[What it accepts](#what-it-accepts) lists every supported kind.

## Configuration

| Env | Default | Meaning |
|---|---|---|
| `STICKER_PACK_NAME` | `stickers_by_<bot username>` | The Telegram set to write to |
| `OWNER_ID` | — | Must be the account that **owns** that set |

`OWNER_ID` is reused rather than given a sticker-specific twin because
`addStickerToSet` takes the **set owner's** user ID, not the caller's, and the
default pack belongs to the bot owner. Point `OWNER_ID` at the owning account if
the configured pack belongs to someone else.

The caller's identity is used nowhere. That is what makes the command stateless:
no records, no keys, no per-user locks, and no ownership checks. It is also why
`/addsticker` needs no per-user storage.

The bot's username comes from `BOT_USERNAME` when set, otherwise from one
`getMe` call at startup (retried on the first `/addsticker` if that failed).

`STICKER_PACK_NAME` **must end in `_by_<this bot's username>`** — Telegram
requires that suffix on every set a bot creates, and refuses to let a bot edit
any set it did not create. A name without the suffix therefore cannot be this
bot's, which makes it a configuration fault the command can prove offline,
before any download or API call:

> The shared pack is not one this bot can manage. Ask the bot owner to check its
> configuration.

## Creating the pack

**The pack creates itself on first use.** If the set does not exist, the first
`/addsticker` creates it — owned by `OWNER_ID`, titled with the slug half of the
name (`stickers_by_examplebot` → *stickers*), and seeded with the sticker that
triggered it, because Telegram cannot create an empty set. The reply says
*"Created the shared pack with this sticker."*

Rename the title afterwards through `@Stickers` if the derived one is not wanted.
The name itself is permanent — Telegram has no method to rename a set's short
name.

The order is add-first, create-on-missing, not probe-then-add: probing with
`getStickerSet` would cost an extra call on every invocation forever, and return
the set's entire sticker list each time, to save one call on the single
invocation that creates the pack.

**A name already taken by a set this bot cannot write to is an error, not a
takeover.** If the add reports the set missing *and* the create reports the name
occupied, something stands there that this bot cannot manage — a set created by
another bot, or by this bot for a different owner:

> A sticker set with that name already exists and this bot cannot manage it. Ask
> the bot owner to check it.

**The owner cannot be verified up front.** `getStickerSet` returns a set's name,
title, type, stickers and thumbnail — and no owner or creator ID. So the suffix
proves *which bot* created a set, but nothing proves *which user* owns it until
Telegram refuses the write. That refusal is what the message above reports.

## No moderation, by design

Anyone who can reach the bot can add to the pack. There is no approval step, no
per-contributor limit, and no way to remove a sticker through the bot —
`/delsticker`, `/editsticker`, `/ordersticker`, `/setpackicon`, `/renamepack`
and `/delpack` were removed along with the per-user model.

Cleanup is done by the pack's owner through Telegram's own `@Stickers` bot,
which can edit any set the owner owns. Two consequences worth accepting on
purpose before enabling this:

- The pack carries the owner's name and sits in the sticker tray of everyone who
  installed it, but its contents are decided by whoever runs the command.
- The 120-sticker ceiling is shared. One user can fill it.

## What it accepts

| Replied message | Result |
|---|---|
| **Sticker** — static, animated (.TGS), or video (.WEBM) | Copied by `file_id`, no conversion |
| **Photo** or image **document** (`image/png`, `image/jpeg`, `image/webp`) | Converted to a 512px PNG and uploaded |
| **Video, GIF, animation, video note** | Transcoded to a WEBM/VP9 video sticker |
| **Mask or custom-emoji sticker** | Refused — the pack is a `regular` set |

**All three sticker formats go in the same pack.** Since Bot API 7.2 the format
is a property of each sticker, not of the set: `createNewStickerSet` lost its
`sticker_format` parameter and `StickerSet` lost `is_animated`/`is_video`. So a
pack seeded with a static sticker takes video stickers later with no migration.

**A sticker is copied, never converted.** It already lives in a set Telegram
accepted, so it satisfies every dimension, duration and size rule for its
format. That is why animated and video *stickers* work here while an ordinary
video or GIF *file* does not. Copying a sticker out of someone else's pack is
normal Telegram behaviour and does not touch that pack.

A replied photo or image document is downloaded, resized so its long edge is
exactly 512px with the aspect ratio preserved, uploaded as a PNG, and then
added — attributed to the pack owner, matching the set it is about to join.
Sources above 2 MB, or with either side above 4096px, are rejected.

**Replying to another bot's image** works only after the
[group setup](deploy-coolify-selfhosted.md#4-group-setup); otherwise Telegram
strips the image out of the reply.

## Video and GIF

A replied video, GIF, animation or video note is downloaded and transcoded with
**ffmpeg** to what Telegram requires of a video sticker: WEBM/VP9, long edge
exactly 512px, at most 3 seconds, at most 30 FPS, at most 256 KB, and **no
audio stream**. Documents count too — `image/gif`, `video/mp4`, `video/webm`,
`video/quicktime`, `video/x-matroska`.

Every one of those rules is applied by the filter chain and encoder flags rather
than checked afterwards, so there is no case where a source slips through
half-converted:

- The long edge is scaled to exactly 512 **in either direction**. A 100×50 GIF
  becomes 512×256 — deliberately not `force_original_aspect_ratio=decrease`,
  which leaves a small source undersized and so fails the "one side must be
  exactly 512" rule.
- `-t 3` cuts the length; `fps=30` caps the rate.
- `-an -sn -dn` drops audio, subtitle and data streams. Telegram refuses a video
  sticker carrying audio, so this is not merely tidiness.
- `-pix_fmt yuva420p` keeps GIF transparency; VP9 carries an alpha plane.

Size is the one rule that cannot be known before encoding, so it retries down a
CRF ladder (32 → 42 → 52) until the output fits. Ordinary footage lands around
30–70 KB at the first rung, well inside the limit. If even the last rung is too
big, the smallest attempt is sent and Telegram is left to be the authority.

`image/gif` is transcoded rather than reduced to its first frame — a GIF is sent
to be animated. (Note that a GIF forwarded through Telegram usually arrives as
an `Animation` in mp4 form, not as `image/gif` at all.)

**ffmpeg is a hard runtime dependency.** Telegram accepts no other codec for a
video sticker, Go's standard library has no VP9 encoder
(`golang.org/x/image/vp8` decodes only), and the binary is built
`CGO_ENABLED=0` so a cgo encoder would not link. The runtime image is therefore
`alpine` with `apk add ffmpeg` rather than `distroless/static`, which cannot
carry a second binary — the one reason that base was given up, and it takes the
image from roughly 20 MB to 213 MB.

**A transcode holds the whole bot.** Handlers run inline on a single worker, so
an encode is time no other user is served. A 1280×720 source encodes in about
0.4 s with these flags, and one encode is capped at 20 s so a pathological input
fails rather than hangs. The moving path also gets a longer handler deadline
(45 s, against 10 s for stills) — chosen from the replied message before any API
call, so a still never pays for the video budget. Source downloads are capped at
10 MB for video against 2 MB for images, since the ceiling is on the input and
the 256 KB limit applies to the output.

Telegram allows 120 stickers per pack and 1–20 emoji per sticker. Emoji come
from the command's arguments, else the replied sticker's own emoji, else `⭐`.
There is no documented file-size limit for static stickers; the code applies its
own client-side ceiling and never presents it as a Telegram rule.

## Behaviour worth knowing

**The reply carries no sticker count.** Nothing is stored, and reading the count
back would mean a `getStickerSet` call that returns the entire set on every add.

**Download errors are never echoed.** A Telegram file URL embeds the bot token,
and every transport failure from the HTTP client formats that URL into its error
text. The download path replaces all of them with one opaque error and logs only
a coarse type label, so no failure mode can print the token.

**Telegram's refusals are translated, not forwarded.** `STICKERS_TOO_MUCH`,
`STICKERSET_INVALID` and the emoji errors get sentences a user can act on;
anything unrecognised becomes a generic line plus an ERROR log.

The handler runs under a 10-second deadline, with the download-and-upload leg
bounded inside it so the reply always has budget left. The bot processes updates
one at a time, so that bound is what keeps an image conversion from stalling
other users.
