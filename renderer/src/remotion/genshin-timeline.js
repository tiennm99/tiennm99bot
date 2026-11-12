/**
 * Timing, palette, and layout for the Genshin-style meteor wish served on
 * /api/genshin. Kept free of React so the choreography can be unit tested.
 *
 * The meteor, the sky, and the rank emblem are a pre-rendered clip per rarity
 * (`public/genshin/wish-<rarity>.mp4`, built by `blender/wish-scene.py`). The
 * composition draws the white flash, the label, and the stars over it, so
 * the times below must match the clip.
 */

/** @typedef {3 | 4 | 5} GenshinRarity */

/**
 * @typedef {object} RarityPalette
 * @property {string} core Hottest highlight; tints the 5★ flash.
 * @property {string} glow Rarity colour of the label glow.
 */

export const genshinTotalSeconds = 7;

/** Landscape frame heights for each requested width: 16:9, rounded to even pixels for H.264. */
export const genshinFrameSizes = Object.freeze({640: 360, 854: 480});

/**
 * Seconds from the start of the clip. The meteor lands at `meteorEnd`, the
 * white flash peaks and holds, and the clip cuts to the emblem reveal at
 * `revealStart`, while the flash is opaque.
 */
export const genshinTimeline = Object.freeze({
  meteorStart: 0.3,
  meteorEnd: 2.5,
  flashStart: 2.3,
  flashPeak: 2.7,
  flashHoldEnd: 2.95,
  flashEnd: 3.5,
  revealStart: 2.95,
  emblemSettled: 3.7,
  nameIn: 3.6,
  nameSettled: 4.1,
  starsStart: 4.1,
  starStep: 0.18,
});

/** Centre of the rank emblem and the meteor's impact, as fractions of the frame. */
export const emblemCenter = Object.freeze({x: 0.66, y: 0.47});

/** Every rarity reveals its stars in the same gold, as in the source game. */
export const starColor = '#ffcc33';

/** @type {Record<GenshinRarity, RarityPalette>} */
const palettes = {
  3: {core: '#f2f8ff', glow: '#6fb6ff'},
  4: {core: '#fbf2ff', glow: '#c88cff'},
  5: {core: '#fffbe8', glow: '#ffd36b'},
};

/**
 * @param {GenshinRarity} rarity
 * @returns {RarityPalette}
 */
export const getRarityPalette = (rarity) => palettes[rarity];

/**
 * Path of the pre-rendered clip for a rarity, relative to `public/`.
 *
 * @param {GenshinRarity} rarity
 * @returns {string}
 */
export const getWishClip = (rarity) => `genshin/wish-${rarity}.mp4`;

/**
 * Seconds at which each reveal star pops in, one per rarity level.
 *
 * @param {GenshinRarity} rarity
 * @returns {number[]}
 */
export const getStarRevealTimes = (rarity) =>
  Array.from({length: rarity}, (_, index) => genshinTimeline.starsStart + index * genshinTimeline.starStep);

/**
 * Font size for the revealed name, shrinking long labels so they fit within
 * two lines on the left half of the frame.
 *
 * @param {string} label
 * @param {number} height
 * @returns {number}
 */
export const getLabelFontSize = (label, height) => {
  const length = Array.from(label).length;
  if (length <= 8) {
    return Math.round(height * 0.12);
  }
  if (length <= 16) {
    return Math.round(height * 0.09);
  }
  if (length <= 28) {
    return Math.round(height * 0.07);
  }
  return Math.round(height * 0.055);
};

/**
 * @typedef {object} TierEffects
 * @property {number} shake     Impact camera shake amplitude, as a fraction of height.
 * @property {number} starFlare Size multiplier of each star's pop flare.
 */

/**
 * The parts of each tier's loudness drawn over the clip; the clip itself
 * scales the meteor, rays, rings, and particles by tier.
 *
 * @type {Record<GenshinRarity, TierEffects>}
 */
const tierEffects = {
  3: {shake: 0, starFlare: 1},
  4: {shake: 0.012, starFlare: 1.4},
  5: {shake: 0.03, starFlare: 2},
};

/**
 * @param {GenshinRarity} rarity
 * @returns {TierEffects}
 */
export const getTierEffects = (rarity) => tierEffects[rarity];

/**
 * Decaying impact shake offset in pixels, zero before `start` and after it
 * settles.
 *
 * @param {number} seconds
 * @param {number} start
 * @param {number} amplitude Pixels at the moment of impact.
 * @returns {{x: number, y: number}}
 */
export const getShakeOffset = (seconds, start, amplitude) => {
  const elapsed = seconds - start;
  if (amplitude <= 0 || elapsed < 0 || elapsed > 0.8) {
    return {x: 0, y: 0};
  }
  const decay = amplitude * (1 - elapsed / 0.8) ** 2;
  return {x: Math.sin(elapsed * 71) * decay, y: Math.cos(elapsed * 53) * decay};
};
