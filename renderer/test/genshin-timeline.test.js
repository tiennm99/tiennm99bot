import {existsSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {describe, expect, test} from 'vitest';
import {
  genshinTimeline,
  genshinTotalSeconds,
  getLabelFontSize,
  getRarityPalette,
  getShakeOffset,
  getStarRevealTimes,
  getTierEffects,
  getWishClip,
} from '../src/remotion/genshin-timeline.js';

describe('genshin timeline', () => {
  test('phases run in order and finish before the clip ends', () => {
    const tl = genshinTimeline;
    expect(tl.meteorStart).toBeLessThan(tl.meteorEnd);
    expect(tl.flashStart).toBeLessThan(tl.flashPeak);
    expect(tl.meteorEnd).toBeLessThanOrEqual(tl.flashPeak);
    expect(tl.flashPeak).toBeLessThan(tl.flashHoldEnd);
    expect(tl.revealStart).toBeLessThanOrEqual(tl.flashHoldEnd);
    expect(tl.flashHoldEnd).toBeLessThan(tl.flashEnd);
    expect(tl.nameIn).toBeLessThan(tl.nameSettled);
    const lastStar = getStarRevealTimes(5).at(-1) ?? 0;
    expect(lastStar + 1).toBeLessThan(genshinTotalSeconds);
  });

  test.each([3, 4, 5])('reveals one star per rarity level for %i stars', (rarity) => {
    const times = getStarRevealTimes(/** @type {3 | 4 | 5} */ (rarity));
    expect(times).toHaveLength(rarity);
    expect(times[0]).toBe(genshinTimeline.starsStart);
    expect([...times].sort((a, b) => a - b)).toEqual(times);
  });

  test('each rarity has a distinct colour', () => {
    const glows = [3, 4, 5].map((rarity) => getRarityPalette(/** @type {3 | 4 | 5} */ (rarity)).glow);
    expect(new Set(glows).size).toBe(3);
  });

  test.each([3, 4, 5])('ships the pre-rendered clip for %i stars', (rarity) => {
    const clip = getWishClip(/** @type {3 | 4 | 5} */ (rarity));
    expect(existsSync(fileURLToPath(new URL(`../public/${clip}`, import.meta.url)))).toBe(true);
  });

  test('long labels get smaller text', () => {
    expect(getLabelFontSize('Pizza', 360)).toBeGreaterThan(getLabelFontSize('x'.repeat(40), 360));
  });

  test('shake and star flares grow with rarity', () => {
    const [three, four, five] = [3, 4, 5].map((rarity) => getTierEffects(/** @type {3 | 4 | 5} */ (rarity)));
    for (const key of /** @type {const} */ (['shake', 'starFlare'])) {
      expect(four?.[key], key).toBeGreaterThan(three?.[key] ?? Infinity);
      expect(five?.[key], key).toBeGreaterThan(four?.[key] ?? Infinity);
    }
  });

  test('impact shake decays to rest', () => {
    expect(getShakeOffset(1, 2, 10)).toEqual({x: 0, y: 0});
    expect(getShakeOffset(3, 2, 10)).toEqual({x: 0, y: 0});
    expect(getShakeOffset(2.1, 2, 0)).toEqual({x: 0, y: 0});
    const early = getShakeOffset(2.05, 2, 10);
    const late = getShakeOffset(2.7, 2, 10);
    expect(Math.hypot(early.x, early.y)).toBeGreaterThan(Math.hypot(late.x, late.y));
  });
});
