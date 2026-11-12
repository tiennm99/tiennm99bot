import {describe, expect, test} from 'vitest';
import {loadConfig, minRenderTimeoutMs} from '../src/config.js';

describe('loadConfig', () => {
  test('keeps render timeout at or above the browser timeout floor', () => {
    const config = loadConfig({RENDERER_RENDER_TIMEOUT_MS: '500'});

    expect(config.renderTimeoutMs).toBe(minRenderTimeoutMs);
  });
});
