#!/usr/bin/env sh
# Regenerates the Blender-made assets the bot embeds, writing them straight
# into each renderer's assets/ folder. Needs Blender 4.2 or newer (Eevee) and
# ffmpeg; set BLENDER to use a Blender that is not on PATH.
#
#   scripts/gen-assets.sh genshin   the /genshin wish clips (several minutes)
#   scripts/gen-assets.sh gacha     the /gacha card-pack clips and tracks (about half an hour)
#   scripts/gen-assets.sh wheel     the /wheelofnames layers (seconds)
#   scripts/gen-assets.sh all       all three
#
# --out DIR writes to DIR instead, to review a change before replacing the
# committed assets.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
blender=${BLENDER:-blender}
out=""

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "gen-assets: $1 not found on PATH" >&2
    exit 1
  }
}

# Scratch frames go under the repo's ignored .tmp/ and are removed on exit.
scratch=""
cleanup() { [ -z "$scratch" ] || rm -rf "$scratch"; }
trap cleanup EXIT INT TERM

genshin() {
  need ffmpeg
  dest=${out:-$root/internal/modules/random/genshin/assets}
  mkdir -p "$dest" "$root/.tmp"
  scratch=$(mktemp -d "$root/.tmp/genshin-frames.XXXXXX")
  for rarity in 3 4 5; do
    "$blender" -b --factory-startup -P "$root/scripts/blender/genshin-wish.py" -- \
      --rarity "$rarity" --out "$scratch/$rarity"
    # +faststart puts the index first, so the bot can decode the clip from a pipe.
    ffmpeg -hide_banner -loglevel error -y -framerate 30 -start_number 1 \
      -i "$scratch/$rarity/frame-%04d.png" \
      -c:v libx264 -preset slow -crf 20 -pix_fmt yuv420p -movflags +faststart \
      "$dest/wish-$rarity.mp4"
    echo "gen-assets: wrote $dest/wish-$rarity.mp4"
  done
}

gacha() {
  need ffmpeg
  dest=${out:-$root/internal/modules/random/cardpack/assets}
  mkdir -p "$dest" "$root/.tmp"
  scratch=$(mktemp -d "$root/.tmp/gacha-frames.XXXXXX")
  for rarity in 3 4 5; do
    "$blender" -b --factory-startup -P "$root/scripts/blender/gacha-pack.py" -- \
      --rarity "$rarity" --out "$scratch/$rarity"
    ffmpeg -hide_banner -loglevel error -y -framerate 30 -start_number 1 \
      -i "$scratch/$rarity/frame-%04d.png" \
      -c:v libx264 -preset slow -crf 20 -pix_fmt yuv420p -movflags +faststart \
      "$dest/pack-$rarity.mp4"
    cp "$scratch/$rarity/track.json" "$dest/track-$rarity.json"
    echo "gen-assets: wrote $dest/pack-$rarity.mp4 and track-$rarity.json"
  done
}

wheel() {
  dest=${out:-$root/internal/modules/random/wheel/assets}
  mkdir -p "$dest"
  "$blender" -b --factory-startup -P "$root/scripts/blender/wheel-layers.py" -- --out "$dest"
  echo "gen-assets: wrote the wheel layers to $dest"
}

while [ $# -gt 0 ]; do
  case $1 in
    --out)
      [ $# -ge 2 ] || usage
      out=$2
      shift 2
      ;;
    -h | --help) usage ;;
    *) break ;;
  esac
done
[ $# -eq 1 ] || usage
need "$blender"

case $1 in
  genshin) genshin ;;
  gacha) gacha ;;
  wheel) wheel ;;
  all)
    genshin
    gacha
    wheel
    ;;
  *) usage ;;
esac
