import {buildServer} from '../src/server.js';

const config = {
  host: '127.0.0.1',
  maxConcurrentRenders: 1,
  maxOptionChars: 40,
  maxOptions: 32,
  port: 0,
  renderTimeoutMs: 30000,
};

const app = await buildServer({config, warmRenderers: true});

try {
  const response = await app.inject({
    method: 'POST',
    url: '/api/gif',
    payload: {
      durationMs: 3000,
      fps: 12,
      holdMs: 500,
      options: ['alice', 'bob', 'carol', 'dave'],
      size: 384,
      theme: 'classic',
      winnerIndex: 2,
    },
  });

  if (response.statusCode !== 200) {
    throw new Error(`Expected 200, got ${response.statusCode}: ${response.body}`);
  }

  const magic = response.rawPayload.subarray(0, 6).toString();
  if (magic !== 'GIF89a') {
    throw new Error(`Expected GIF89a header, got ${magic}`);
  }

  if (response.headers['x-wheel-winner-index'] !== '2') {
    throw new Error(`Expected winner index 2, got ${response.headers['x-wheel-winner-index']}`);
  }

  console.log(`api-smoke: ${response.rawPayload.byteLength} bytes`);

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
