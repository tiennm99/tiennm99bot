#!/usr/bin/env sh
# Renders demo clips of the animations the bot draws in process into the
# repo's ignored .tmp/demo/, with a contact sheet beside each clip. Needs Go,
# ffmpeg, and git.
#
#   scripts/demo.sh [flags]                render the working tree into .tmp/demo/current
#   scripts/demo.sh compare <ref> [flags]  also render <ref>, then put each pair
#                                          side by side in .tmp/demo/compare
#
# Flags go to cmd/animdemo: -only wheel,genshin,gacha,csgo and -seed N. A compare
# needs cmd/animdemo at <ref> too.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

if [ "${1:-}" != compare ]; then
  case ${1:-} in -h | --help) usage ;; esac
  rm -rf .tmp/demo/current
  exec go run ./cmd/animdemo -out .tmp/demo/current "$@"
fi

[ $# -ge 2 ] || usage
ref=$2
shift 2
base=$root/.tmp/demo/compare
tree=$root/.tmp/demo-worktree
rm -rf "$base"
mkdir -p "$base"

git worktree remove --force "$tree" 2>/dev/null || true
git worktree add --detach --quiet "$tree" "$ref"
trap 'git worktree remove --force "$tree"' EXIT INT TERM

echo "demo: rendering the working tree"
go run ./cmd/animdemo -out "$base/after" "$@"
echo "demo: rendering $ref"
(cd "$tree" && go run ./cmd/animdemo -out "$base/before" "$@")

for after in "$base"/after/*.mp4; do
  name=$(basename "$after")
  before=$base/before/$name
  if [ ! -f "$before" ]; then
    echo "demo: $ref has no $name; skipped"
    continue
  fi
  ffmpeg -hide_banner -loglevel error -y -i "$before" -i "$after" -filter_complex \
    "[0:v]scale=-2:360,drawtext=text='before':x=12:y=10:fontsize=22:fontcolor=white:box=1:boxcolor=black@0.55:boxborderw=6[a];[1:v]scale=-2:360,drawtext=text='after':x=12:y=10:fontsize=22:fontcolor=white:box=1:boxcolor=black@0.55:boxborderw=6[b];[a][b]hstack" \
    -c:v libx264 -crf 20 -pix_fmt yuv420p "$base/$name"
  echo "demo: $base/$name"
done
