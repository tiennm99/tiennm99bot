// Browser page for the gacha wish: one pack-cards pack whose single card shows
// the rolled label, rank, and stars. The renderer injects `window.gachaWish`
// before this module runs, tears the pack open with a scripted drag, and
// captures each frame.
import {createPackView} from 'pack-cards';

/**
 * @typedef {object} GachaWishPage
 * @property {string} label
 * @property {string} rank       Rank letter, as on /api/gacha.
 * @property {number} stars
 * @property {string} rarity     pack-cards rarity profile.
 * @property {'silver' | 'platinum' | 'gold'} glow  pack-cards glow preset.
 * @property {{accent: string, tint: string}} artwork
 */

const wish = /** @type {GachaWishPage} */ (/** @type {any} */ (window).gachaWish);
const stage = /** @type {HTMLElement} */ (document.getElementById('stage'));
stage.style.setProperty('--wish-accent', wish.artwork.accent);
stage.style.setProperty('--wish-tint', wish.artwork.tint);

/**
 * @param {string} tag
 * @param {string} className
 * @param {string} [text]
 */
const el = (tag, className, text) => {
  const node = document.createElement(tag);
  node.className = className;
  if (text !== undefined) {
    node.textContent = text;
  }
  return node;
};

const createFace = () => {
  const face = el('article', 'wish-card');
  const content = el('div', 'recap-card-content');
  const title = el('h2', 'wish-label', wish.label);
  title.setAttribute('data-foil-text', '');
  content.append(el('span', 'wish-rank', wish.rank), title, el('p', 'wish-stars', '★'.repeat(wish.stars)));
  const clip = el('span', 'wish-glint-clip');
  clip.setAttribute('aria-hidden', 'true');
  // Set on the clip itself: the card flies in an overlay outside #stage.
  clip.style.setProperty('--glint', wish.artwork.accent);
  clip.append(el('span', 'wish-glint'));
  face.append(content, clip);
  return face;
};

/**
 * pack-cards (pinned commit 83e8fbb) flies the revealed card out of the pack
 * over 1750ms: the card is clear of the pack at 900ms, then it flips with a
 * half turn (rotateY 180deg to 0deg) and the pack drops away while it glides
 * to the centre. The page reshapes those animations without changing the
 * library: the rise out of the pack eases exponentially in and out,
 * everything after the card clears the pack is stretched so the flip has time
 * for three extra turns, and the flip eases out like a flicked card slowing to
 * a stop while sparkles burst around it. The spin lands flat a moment before
 * the card stops gliding: Chrome redraws text on a slightly tilted card
 * differently from a flat one, so ending both together made the text hop
 * after the card looked still.
 */
/**
 * The flight reaches its final pose `settleMs` (of the original 1750ms)
 * early, skipping a sub-pixel creep: the text drawn on the card's last scaled
 * frames differs from the text once the card is laid out at rest, so it
 * visibly hopped when the card landed.
 */
const flight = Object.freeze({duration: 1750, clearAt: 900, settleMs: 60});
/** Exponential ease-in-out, as a CSS timing function. */
const easeInOutExpo = 'cubic-bezier(.87, 0, .13, 1)';
const afterClearStretch = 1.65;
/** The flip's ease-out, as the control points of a CSS cubic-bezier. */
const flipCurve = Object.freeze({x1: 0.3, y1: 0.4, x2: 0.25, y2: 1});
const flip = Object.freeze({
  startDegrees: 180 + 3 * 360,
  easing: `cubic-bezier(${flipCurve.x1}, ${flipCurve.y1}, ${flipCurve.x2}, ${flipCurve.y2})`,
  flatAt: 0.8,
});

/**
 * The fraction of the spin at which the card turns edge-on for the last
 * time, so its face swings into view for good: where the flip's ease has
 * covered all but the final 90 degrees.
 */
const lastFaceUpAt = (() => {
  const {x1, y1, x2, y2} = flipCurve;
  const coordinate = (/** @type {number} */ t, /** @type {number} */ a, /** @type {number} */ b) =>
    3 * a * t * (1 - t) ** 2 + 3 * b * t ** 2 * (1 - t) + t ** 3;
  const target = 1 - 90 / flip.startDegrees;
  // Both bezier coordinates rise monotonically with t, so bisect on t.
  let low = 0;
  let high = 1;
  for (let step = 0; step < 40; step += 1) {
    const middle = (low + high) / 2;
    if (coordinate(middle, y1, y2) < target) {
      low = middle;
    } else {
      high = middle;
    }
  }
  return coordinate(low, x1, x2) * flip.flatAt;
})();

/**
 * Maps a time in the original flight to the stretched one.
 *
 * @param {number} ms
 */
const stretch = (ms) => (ms <= flight.clearAt ? ms : flight.clearAt + (ms - flight.clearAt) * afterClearStretch);

const sparkleColors = [wish.artwork.accent, '#ffffff', '#fff0b8'];

/**
 * Bursts small four-point sparkles out from the card while it spins. They
 * sit beside the turning face inside the card's deck, so they travel with
 * the card without spinning.
 *
 * @param {Element} deck
 * @param {number} delay     Milliseconds until the spin starts.
 * @param {number} duration  Length of the spin.
 */
const burstSparkles = (deck, delay, duration) => {
  const layer = el('div', 'wish-sparkles');
  deck.append(layer);
  const size = deck.getBoundingClientRect().width || 200;
  for (let index = 0; index < 72; index += 1) {
    const sparkle = el('span', 'wish-sparkle');
    const angle = Math.random() * Math.PI * 2;
    const reach = size * (0.55 + Math.random() * 0.75);
    const scale = 0.6 + Math.random() * 0.9;
    sparkle.style.setProperty('--sparkle', sparkleColors[index % sparkleColors.length] ?? '#ffffff');
    const dx = Math.cos(angle) * reach;
    const dy = Math.sin(angle) * reach * 1.3;
    sparkle.animate(
      [
        {transform: 'translate(-50%, -50%) scale(0) rotate(0deg)', opacity: 0},
        {
          transform: `translate(calc(-50% + ${dx * 0.45}px), calc(-50% + ${dy * 0.45}px)) scale(${scale}) rotate(45deg)`,
          opacity: 1,
          offset: 0.25,
        },
        {transform: `translate(calc(-50% + ${dx}px), calc(-50% + ${dy}px)) scale(0) rotate(90deg)`, opacity: 0},
      ],
      {
        // Squaring front-loads the burst as the spin begins, then it thins out.
        delay: delay + Math.random() ** 2 * duration * 0.8,
        duration: 500 + Math.random() * 500,
        easing: 'cubic-bezier(.2, .7, .3, 1)',
        fill: 'both',
      },
    );
    layer.append(sparkle);
  }
  setTimeout(() => layer.remove(), delay + duration + 1200);
};

/** The glint moves on ease-in-out. */
const glint = Object.freeze({duration: 1800, easing: 'ease-in-out'});

/**
 * Keeps the card's text hidden while it spins and switches it on as the card
 * turns edge-on for its last swing face up, so the text rides in on the final
 * turn without visibly popping on. Then it sweeps a mirror glint from the
 * top-left corner to the bottom-right one as the card comes to rest. The glint
 * band is a gradient across a box three times the card's size and centred on
 * it, so the card stays inside that box for the whole sweep and every corner is
 * lit; a card-sized box would leave the top-right and bottom-left corners
 * outside it as it slid diagonally. The box moves 0.85 card-widths and heights
 * each way, so both streaks (the thin one trails the wide one) start and end
 * clear of the card.
 *
 * @param {Element} turn
 * @param {number} delay     Milliseconds until the spin starts.
 * @param {number} duration  Length of the spin.
 */
const revealFace = (turn, delay, duration) => {
  turn.querySelector('.wish-card .recap-card-content')?.animate(
    [{opacity: 0}, {opacity: 0, offset: lastFaceUpAt}, {opacity: 1, offset: lastFaceUpAt}, {opacity: 1}],
    {delay, duration, easing: 'linear', fill: 'both'},
  );
  turn.querySelector('.wish-glint')?.animate([{transform: 'translate(-28.3%, -28.3%)'}, {transform: 'translate(28.3%, 28.3%)'}], {
    delay: delay + duration,
    duration: glint.duration,
    easing: glint.easing,
    fill: 'both',
  });
};

/**
 * Engravings carry the polychrome (page.css): 4★ cards are tooled with the
 * epic contour, 5★ cards with the legendary rings or facets, picked by
 * pack-cards from the card's label. 3★ cards stay plain.
 *
 * @type {import('pack-cards').AppearanceSettingsOptions['rarities']}
 */
const engravings = Object.freeze({
  epic: {engraving: 'contour'},
  legendary: {engraving: ['radial', 'facets']},
});

const animate = Element.prototype.animate;
/**
 * @this {Element}
 * @param {Keyframe[] | PropertyIndexedKeyframes | null} keyframes
 * @param {number | KeyframeAnimationOptions} [options]
 */
Element.prototype.animate = function (keyframes, options) {
  if (!Array.isArray(keyframes) || typeof options !== 'object') {
    return animate.call(this, keyframes, options);
  }
  // The flight path: keyframes spaced evenly over the whole flight.
  if (options.duration === flight.duration && options.easing === 'linear') {
    const total = stretch(flight.duration);
    const at = (/** @type {Keyframe} */ frame) => Number(frame.offset) * flight.duration;
    // Until the card clears the pack it only translates, so the rise is one
    // straight segment: keep its two ends and ease between them.
    const cleared = keyframes.findIndex((frame) => at(frame) > flight.clearAt);
    const [start] = keyframes;
    const eased =
      start && cleared > 1 ? [{...start, easing: easeInOutExpo}, ...keyframes.slice(cleared - 1)] : keyframes;
    const rest = eased.at(-1);
    const stretched = eased.map((frame) => ({
      ...frame,
      ...(rest && at(frame) >= flight.duration - flight.settleMs ? {transform: rest.transform} : {}),
      offset: stretch(at(frame)) / total,
    }));
    return animate.call(this, stretched, {...options, duration: total});
  }
  // The flip, the pack dropping away, and the spreading backs all start as the card clears the pack.
  if (options.delay === flight.clearAt && options.duration === flight.duration - flight.clearAt) {
    const duration = stretch(flight.duration) - flight.clearAt;
    const isFlip =
      keyframes.length === 2 &&
      keyframes[0]?.transform === 'rotateY(180deg)' &&
      keyframes[1]?.transform === 'rotateY(0deg)';
    if (!isFlip) {
      return animate.call(this, keyframes, {...options, duration});
    }
    if (this.parentElement) {
      burstSparkles(this.parentElement, flight.clearAt, duration);
    }
    revealFace(this, flight.clearAt, duration);
    const flat = keyframes[1] ?? {};
    return animate.call(
      this,
      [{transform: `rotateY(${flip.startDegrees}deg)`, easing: flip.easing}, {...flat, offset: flip.flatAt}, flat],
      {...options, duration, easing: 'linear'},
    );
  }
  return animate.call(this, keyframes, options);
};

// Blank label and title keep the pack wrapper free of printed text.
/**
 * The wish's emblem: the symbol of pack-cards' example Moon card (☽), flipped
 * to its mirror-image crescent.
 */
const moon = '☾';

/**
 * The moon as an image for the pack, which pack-cards prints in place of its
 * default ✦ when the artwork carries a logo. It must be decoded before the
 * pack draws its artwork.
 */
const moonLogo = new Image(132, 132);
moonLogo.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(
  `<svg xmlns="http://www.w3.org/2000/svg" width="132" height="132" viewBox="0 0 132 132"><text x="66" y="66" text-anchor="middle" dominant-baseline="central" font-family="sans-serif" font-size="120" fill="${wish.artwork.accent}">${moon}</text></svg>`,
)}`;
await moonLogo.decode();

/**
 * The card back, in pack-cards' own print layout: the moon sits in the
 * diamond seal in place of the default ✦, over the bot's name.
 */
const renderBack = () => {
  const print = el('div', 'recap-card-print');
  const seal = el('span', 'recap-print-seal wish-moon-seal');
  seal.append(el('span', '', moon));
  print.append(el('span', 'recap-print-channel'), seal, el('span', 'recap-print-edition', 'tiennm99bot'));
  return print;
};

const view = createPackView(stage, {
  label: '',
  artwork: {...wish.artwork, logo: moonLogo},
  labels: {title: '', swipe: '', tap: ''},
  renderBack,
  appearance: {opening: 'animated', motion: 'interactive', glow: wish.glow, pack_zoom: true, rarities: engravings},
});

view.showPack({
  count: 1,
  renderCard: () => ({face: createFace(), identity: wish.label, rarity: wish.rarity}),
  onReveal: () => {
    document.documentElement.dataset.wish = 'revealed';
  },
  onError: (error) => {
    document.documentElement.dataset.wish = 'failed';
    console.error(error);
  },
});
