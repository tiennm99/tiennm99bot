#!/usr/bin/env sh
# Renders the three /genshin wish clips with Blender and encodes them into
# internal/modules/random/genshin/assets/wish-<rarity>.mp4, which the bot
# embeds. Needs blender and ffmpeg on PATH.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
out="$here/../../internal/modules/random/genshin/assets"
frames=$(mktemp -d)
trap 'rm -rf "$frames"' EXIT
mkdir -p "$out"

for rarity in 3 4 5; do
  mkdir -p "$frames/$rarity"
  blender -b --factory-startup -P "$here/wish-scene.py" -- --rarity "$rarity" --out "$frames/$rarity"
  ffmpeg -loglevel error -y -framerate 30 -start_number 1 -i "$frames/$rarity/frame-%04d.png" \
    -c:v libx264 -preset slow -crf 20 -pix_fmt yuv420p -movflags +faststart "$out/wish-$rarity.mp4"
  echo "wrote $out/wish-$rarity.mp4"
done
