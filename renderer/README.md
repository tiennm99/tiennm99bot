# renderer

Part of tiennm99bot: the service behind `/gacha`, deployed by the root
`compose.yml` as the `renderer` service. Self-hosted API that renders card-pack
gacha wish MP4 animations with pack-cards in headless Chrome. (`/wheelofnames`,
`/genshin`, and `/csgo` are rendered by the bot itself; see
`internal/modules/random/`.)

## API


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
with `X-Gacha-Rarity` and `X-Render-Duration-Ms` headers. Renders share the
`RENDERER_MAX_CONCURRENT_RENDERS` slots. No game assets are used.

pack-cards animates on the browser clock. `src/render/render-gacha.js` keeps
one headless Chrome per
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

### Generate fixtures locally

Render the 5★ smoke fixture at the git-ignored path `fixtures/gacha-5-star.mp4`:

```sh
npm run render:smoke
```

Render all three rarities, `fixtures/gacha-{3,4,5}-star.mp4`:

```sh
npm run render:fixtures
```

### Verify

Run the API smoke test and quality gates:

```sh
npm run api:smoke
npm run lint
npm run typecheck
npm test
```

## Deploy

Use a container runtime. Static-only platforms cannot serve `POST /api/gacha`,
which needs Node, headless Chrome with its runtime libraries, and ffmpeg.

```sh
docker build -t tiennm99bot-renderer .
docker run --rm -p 3000:3000 tiennm99bot-renderer
```

Recommended starting resources: 1-2 vCPU and 1-2 GB RAM, with
`RENDERER_MAX_CONCURRENT_RENDERS=1`. `RENDERER_RENDER_TIMEOUT_MS` defaults to
`30000` and is raised to the `7000ms` browser timeout floor when configured lower.
The API has no authentication; publish its port only on a trusted network.
