import {buildServer} from '../src/server.js';

const config = {
  host: '127.0.0.1',
  maxConcurrentRenders: 1,
  maxOptionChars: 40,
  port: 0,
  renderTimeoutMs: 30000,
};

const app = await buildServer({config, warmRenderers: true});

try {
  const gacha = await app.inject({
    method: 'POST',
    url: '/api/gacha',
    payload: {label: 'Bún bò', rarity: 5},
  });

  if (gacha.statusCode !== 200) {
    throw new Error(`Expected gacha 200, got ${gacha.statusCode}: ${gacha.body}`);
  }

  const box = gacha.rawPayload.subarray(4, 8).toString();
  if (box !== 'ftyp') {
    throw new Error(`Expected MP4 ftyp box, got ${box}`);
  }

  console.log(`api-smoke gacha: ${gacha.rawPayload.byteLength} bytes`);

} finally {
  await app.close();
}
