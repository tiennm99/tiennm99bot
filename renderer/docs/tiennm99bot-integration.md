# tiennm99bot Integration

tiennm99bot's `/wheelofnames` renders its wheel with this service, configured
by `RENDERER_URL`, the service's base URL. The service is internal to
the compose network, so requests carry no credentials. Winner handling stays
explicit:

1. The bot parses the comma-separated options.
2. It chooses `winnerIndex` itself.
3. It calls `POST /api/gif`.
4. It sends the response bytes with Telegram `sendAnimation`.
5. Its caption uses its own winner as the source of truth.
6. If the service is not configured or fails, it replies with the winner as
   text.

## Request

Send the JSON body:

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

## Response

- Body: `image/gif`
- Headers:
  - `X-Wheel-Winner-Index`
  - `X-Wheel-Winner`
  - `X-Render-Duration-Ms`

`X-Wheel-Winner` is URL-encoded. Decode it before displaying if service-side
winner selection is used.

## Gacha

`/gacha` in tiennm99bot calls `POST /api/gacha` on the same service.
The bot appends the route to `RENDERER_URL`, picks the result and rarity
itself, and sends the MP4 with `sendAnimation`. On any failure it falls back to
a text reply.

The unlisted `/genshin` command works the same way against `POST /api/genshin`
and sends the 7-second 640x360 meteor wish.
