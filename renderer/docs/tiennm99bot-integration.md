# tiennm99bot Integration

tiennm99bot's `/gacha` renders its wish with this service, configured by
`RENDERER_URL`, the service's base URL. The service is internal to the compose
network, so requests carry no credentials. Result handling stays explicit:

1. The bot parses the comma-separated options and their rarity prefixes.
2. It picks the winner and its rarity itself.
3. It calls `POST /api/gacha` with the winner's label and rarity.
4. It sends the MP4 with Telegram `sendAnimation`, its caption carrying the
   bot's own result.
5. If the service is not configured or fails, it replies with the result as
   text.

`/wheelofnames`, `/genshin`, and `/csgo` do not use this service: the bot
renders them itself with ffmpeg.
