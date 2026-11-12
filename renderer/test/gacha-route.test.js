import {describe, expect, test} from 'vitest';
import {RenderTimeoutError} from '../src/lib/render-errors.js';
import {buildServer} from '../src/server.js';

/** @type {import('../src/config.js').AppConfig} */
const config = {
  host: '127.0.0.1',
  maxConcurrentRenders: 1,
  maxOptionChars: 40,
  port: 0,
  renderTimeoutMs: 15000,
};

const mp4 = Buffer.from('\0\0\0\x18ftypisom-test');

/**
 * @param {Partial<import('../src/config.js').AppConfig>} [overrides]
 * @param {Parameters<typeof buildServer>[0]} [options]
 */
const build = (overrides = {}, options = {}) =>
  buildServer({
    config: {...config, ...overrides},
    renderGacha: async () => ({buffer: mp4, byteLength: mp4.byteLength, durationMs: 9}),
    ...options,
  });

describe('POST /api/gacha', () => {
  test('returns mp4 bytes with rarity metadata', async () => {
    /** @type {unknown} */
    let rendered;
    const app = await build(
      {},
      {
        renderGacha: async (request) => {
          rendered = request;
          return {buffer: mp4, byteLength: mp4.byteLength, durationMs: 9};
        },
      },
    );

    const response = await app.inject({method: 'POST', url: '/api/gacha', payload: {label: 'Pizza', rarity: 5}});

    expect(response.statusCode).toBe(200);
    expect(response.headers['content-type']).toBe('video/mp4');
    expect(response.headers['x-gacha-rarity']).toBe('5');
    expect(response.rawPayload.subarray(4, 8).toString()).toBe('ftyp');
    expect(rendered).toMatchObject({label: 'Pizza', rarity: 5, fps: 24, width: 640});
    expect(Number.isInteger(/** @type {{seed: number}} */ (rendered).seed)).toBe(true);

    await app.close();
  });

  test('picks a different seed per roll unless one is given', async () => {
    /** @type {number[]} */
    const seeds = [];
    const app = await build(
      {},
      {
        renderGacha: async (request) => {
          seeds.push(request.seed);
          return {buffer: mp4, byteLength: mp4.byteLength, durationMs: 9};
        },
      },
    );

    for (let index = 0; index < 3; index += 1) {
      await app.inject({method: 'POST', url: '/api/gacha', payload: {label: 'Pizza', rarity: 5}});
    }
    await app.inject({method: 'POST', url: '/api/gacha', payload: {label: 'Pizza', rarity: 5, seed: 5}});

    expect(new Set(seeds.slice(0, 3)).size).toBeGreaterThan(1);
    expect(seeds[3]).toBe(5);

    await app.close();
  });

  test('rejects an invalid rarity', async () => {
    const app = await build();

    const response = await app.inject({method: 'POST', url: '/api/gacha', payload: {label: 'Pizza', rarity: 6}});

    expect(response.statusCode).toBe(400);
    expect(response.json().error).toBe('invalid_request');

    await app.close();
  });

  test('rejects labels longer than the configured limit', async () => {
    const app = await build({maxOptionChars: 4});

    const response = await app.inject({method: 'POST', url: '/api/gacha', payload: {label: 'Pizza', rarity: 3}});

    expect(response.statusCode).toBe(400);

    await app.close();
  });

  test('returns timeout response when rendering exceeds configured limit', async () => {
    const app = await build(
      {renderTimeoutMs: 7},
      {
        renderGacha: async (_request, renderOptions) => {
          throw new RenderTimeoutError(renderOptions.timeoutInMilliseconds);
        },
      },
    );

    const response = await app.inject({method: 'POST', url: '/api/gacha', payload: {label: 'Pizza', rarity: 4}});

    expect(response.statusCode).toBe(504);
    expect(response.json().error).toBe('render_timeout');

    await app.close();
  });

  test('refuses a second render while the slot is busy', async () => {
    /** @type {() => void} */
    let finishGacha = () => {};
    const app = await build(
      {},
      {
        renderGacha: () =>
          new Promise((resolve) => {
            finishGacha = () => resolve({buffer: mp4, byteLength: mp4.byteLength, durationMs: 9});
          }),
      },
    );

    const pending = app.inject({method: 'POST', url: '/api/gacha', payload: {label: 'Pizza', rarity: 3}});
    await new Promise((resolve) => setTimeout(resolve, 20));
    const busy = await app.inject({method: 'POST', url: '/api/gacha', payload: {label: 'Pho', rarity: 4}});
    finishGacha();

    expect(busy.statusCode).toBe(429);
    expect((await pending).statusCode).toBe(200);

    await app.close();
  });

});
