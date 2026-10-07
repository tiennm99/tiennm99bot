# renderer

Part of tiennm99bot: the service behind `/wheelofnames`, `/gacha`, and
`/genshin`, deployed by the root `compose.yml` as the `renderer` service.
Self-hosted API that renders wheel-of-names GIF animations and Genshin-style
meteor wish MP4 animations with Remotion, and card-pack gacha wish MP4
animations with pack-cards.

## API

```http
POST /api/gif
Content-Type: application/json
Accept: image/gif
```

```json
{
  "options": ["alice", "bob", "carol"],
  "winnerIndex": 1,
  "durationMs": 6500,
  "holdMs": 1200,
  "fps": 15,
  "size": 512,
  "theme": "classic"
}
```

Response is `image/gif` with winner metadata headers:

- `X-Wheel-Winner-Index`
- `X-Wheel-Winner`
- `X-Render-Duration-Ms`

`X-Wheel-Winner` is URL-encoded so non-ASCII labels are safe in HTTP headers.

### Gacha wish

```http
POST /api/gacha
Content-Type: application/json
Accept: video/mp4
```

```json
{
  "label": "Bún bò",
  "rarity": 5,
  "fps": 24,
  "width": 640
}
```

Renders a 6-second portrait wish: a collectible card pack from
[pack-cards](https://github.com/paubineau/pack-cards) is torn open, its card
spins out with a burst of sparkles, and lands showing the request's `label`,
its rank, and its stars. `rarity` (required) picks the rank (`B` for 3★, `A`
for 4★, `S` for 5★), the star count, the card's material (rare, epic,
legendary), and the colour (blue, purple, gold). 4★ and 5★ cards are engraved
with pack-cards' rarity textures (contour on 4★; rings or facets on 5★, chosen
by the label) drawn in a still polychrome rainbow; 3★ cards are plain.
`fps` is `24` or `30`. `width` is the long edge of the portrait frame:
`640` renders 360×640 and `854` renders 480×854. Optional `seed` (integer,
`0` to `2147483647`) seeds the page's randomness; when omitted the service
picks a random one, so every roll looks different. The caller chooses the
result and its rarity — the service only draws it.

Response is a silent H.264 `video/mp4` (Telegram plays it as an animation)
with `X-Gacha-Rarity` and `X-Render-Duration-Ms` headers. All routes share the
`RENDERER_MAX_CONCURRENT_RENDERS` slots. No game assets are used.

pack-cards animates on the browser clock, so the wish does not use Remotion
compositions. `src/render/render-gacha.js` keeps one headless Chrome per
server process in `--deterministic-mode`, steps virtual time one frame at a
time, tears the pack with a scripted drag, captures each frame, and encodes
them with Remotion's bundled ffmpeg. The page (`src/gacha/page/`) and the
package are served from disk; the page has no network access. The first render
compiles the pack's WebGL shaders in software and the first card of each
engraving rasterises its texture, which takes several seconds, so server
start-up renders a throwaway 5★ and 4★ wish first.

pack-cards has no npm release, so it is installed from a GitHub tarball pinned
to a commit. A moving branch URL would change the tarball's checksum and break
`npm ci` against the lockfile, and the Docker image has no `git` for a git
dependency.

### Genshin wish

`POST /api/genshin` takes the same body as `/api/gacha` and returns the same
response, rendering a 7-second landscape wish in the style of a gacha game: a
meteor coloured by rarity (blue 3★, purple 4★, gold 5★) flies in from the left
across a night sky and bursts in a white flash where the rank emblem appears,
and the label is revealed beside a rank emblem (`B`, `A`, `S`) with its stars
popping in. Each tier is louder than the one below: 4★ adds a bigger meteor, a
lens flare, impact shake, and a double shockwave; 5★ adds a rainbow sunburst
before landing, a gold sky flood, a starburst, counter rotating rays, falling
sparkles, and a sheen across the emblem. The per-tier table lives in
`src/remotion/genshin-timeline.js`. `width` is `640` (360 tall) or `854` (480
tall), and `seed` lays out the twinkling stars and particles. All visuals are
drawn procedurally; no game assets are used.

## Local

Install dependencies and Chromium once:

```sh
npm install
npm run browser:ensure
```

Start the local API:

```sh
npm run dev
```

### Generate GIF files locally

Generate the quick smoke fixtures at the git-ignored paths
`fixtures/smoke.gif`, `fixtures/genshin-5-star.mp4`, and `fixtures/gacha-5-star.mp4`:

```sh
npm run render:smoke
```

Generate the complete fixture set at `fixtures/smoke.gif`,
`fixtures/vietnamese.gif`, `fixtures/sixteen-options.gif`, and
`fixtures/genshin-{3,4,5}-star.mp4`, and `fixtures/gacha-{3,4,5}-star.mp4`:

```sh
npm run render:fixtures
```

Render a custom GIF directly without starting the API server:

```powershell
npm run render:local -- `
  --output wheel.gif `
  --option "Chiều nay uống CraneTea" `
  --option "Chiều nay uống CraneTea" `
  --option "Cà phê" `
  --winner 1
```

macOS, Linux, or Git Bash:

```sh
npm run render:local -- \
  --output wheel.gif \
  --option "Chiều nay uống CraneTea" \
  --option "Chiều nay uống CraneTea" \
  --option "Cà phê" \
  --winner 1
```

`--winner` is a zero-based index and is random when omitted. Run
`npm run render:local -- --help` for duration, hold, FPS, size, theme, and timeout
options. The documented root `wheel.gif` and `fixtures/*.gif`/`*.mp4` outputs are
git-ignored and safe to delete; custom output paths may need their own ignore
rule.

### Verify

Run the API smoke test and quality gates:

```sh
npm run api:smoke
npm run lint
npm run typecheck
npm test
```

## Deploy

Use a container runtime first. Static-only platforms cannot satisfy
`POST /api/gif` because Remotion server rendering needs Node, Chromium/runtime
dependencies, and FFmpeg/compositor support.

```sh
docker build -t tiennm99bot-renderer .
docker run --rm -p 3000:3000 tiennm99bot-renderer
```

Recommended starting resources: 1-2 vCPU and 1-2 GB RAM, with
`RENDERER_MAX_CONCURRENT_RENDERS=1`. `RENDERER_RENDER_TIMEOUT_MS` defaults to
`30000` and is raised to Remotion's `7000ms` browser timeout floor when configured lower.
The API has no authentication; publish its port only on a trusted network.
