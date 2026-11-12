# Animations

The bot renders four animated replies, all in the bot itself, encoded with the
image's ffmpeg. No other service is involved.

| Command | Drawn by | Made from |
| --- | --- | --- |
| `/wheelofnames` | `internal/modules/random/wheel` | Blender layers (stage, rim, hub, pointer, peg, light and clear-coat maps) plus segments, labels, bulbs, and confetti drawn in Go. The spin is a physics simulation: a hand push, friction and drag, pegs striking a sprung flapper, sometimes a rock back off the last peg |
| `/genshin` | `internal/modules/random/genshin` | One Blender clip per rarity (meteor over a night sea, then the 3D rank emblem) with the flash, shake, label, and stars drawn over it in Go |
| `/gacha` | `internal/modules/random/cardpack` | One Blender clip per rarity (a foil pack with a moon-and-cat emblem turns once, is torn down its right side, and slides off the card, which spins and lands face up with its rank letter and stars on a guilloché face) plus a per-frame track of the card's corners. Go warps the label onto the moving card with it, lit by the face's shine sweep, and adds the flashes and glints |
| `/csgo` | `internal/modules/random/caseopen` | Drawn entirely in Go: a reel of item cards with motion blur |

Shared drawing (the embedded Noto Sans font, text layout and glow, stars, the
ffmpeg encoder) lives in `internal/modules/random/frames`. Every clip is a
silent H.264 MP4, which Telegram plays as an animation.

## Regenerate the Blender assets

The Blender output is committed in each package's `assets/` folder, so
building and running the bot never needs Blender. Regenerate it only after
changing a scene in `scripts/blender/`:

```sh
scripts/gen-assets.sh wheel     # seconds
scripts/gen-assets.sh genshin   # several minutes
scripts/gen-assets.sh gacha     # about half an hour
scripts/gen-assets.sh --out .tmp/assets wheel   # review before replacing
```

It needs Blender 4.2 or newer (Eevee) and ffmpeg; set `BLENDER` to a Blender
that is not on `PATH`. Geometry and timing shared between a scene and its Go
renderer are noted at the top of both files; change them together.

## Review a change

`scripts/demo.sh` renders sample clips of the in-process animations,
with a contact sheet beside each, into the ignored `.tmp/demo/`:

```sh
scripts/demo.sh                          # .tmp/demo/current
scripts/demo.sh -only wheel              # one kind: wheel, genshin, gacha, or csgo
scripts/demo.sh compare HEAD~1           # before and after, side by side
```

`compare` renders the given git ref in a temporary worktree and writes one
side-by-side clip per sample to `.tmp/demo/compare/`. The ref must already
contain `cmd/animdemo`. Samples share a seed (`-seed`, default 1), so two runs
of unchanged code match.
