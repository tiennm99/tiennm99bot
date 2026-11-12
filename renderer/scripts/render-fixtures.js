import {mkdir, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {closeGachaBrowser, renderGachaVideo} from '../src/render/render-gacha.js';
import {renderWheelGif} from '../src/render/render-gif.js';

const smoke = process.argv.includes('--smoke');
const fixturesDir = path.join(process.cwd(), 'fixtures');

/** @type {{name: string, request: import('../src/schemas/wheel-request.js').WheelRenderRequest}[]} */
const fixtures = [
  {
    name: 'smoke',
    request: {
      options: ['alpha', 'beta', 'gamma', 'delta'],
      winnerIndex: 1,
      durationMs: smoke ? 3000 : 6500,
      holdMs: smoke ? 500 : 1200,
      fps: 12,
      size: smoke ? 384 : 512,
      theme: 'classic',
    },
  },
  {
    name: 'vietnamese',
    request: {
      options: ['khong dau', 'co dau', 'banh mi', 'ca phe sua da', 'pho bo', 'goi cuon', 'ha noi', 'sai gon'],
      winnerIndex: 3,
      durationMs: 6500,
      holdMs: 1200,
      fps: 15,
      size: 512,
      theme: 'festival',
    },
  },
  {
    name: 'sixteen-options',
    request: {
      options: Array.from({length: 16}, (_, index) => `option ${index + 1}`),
      winnerIndex: 9,
      durationMs: 6500,
      holdMs: 1200,
      fps: 15,
      size: 512,
      theme: 'classic',
    },
  },
];

await mkdir(fixturesDir, {recursive: true});

/** @type {{name: string, request: import('../src/schemas/gacha-request.js').GachaRenderRequest}[]} */
const gachaFixtures = [
  {name: 'gacha-5-star', request: {label: 'Bún bò', rarity: 5, fps: 24, width: 640, seed: 7}},
  {name: 'gacha-4-star', request: {label: 'Phở bò tái chín', rarity: 4, fps: 24, width: 640, seed: 11}},
  {name: 'gacha-3-star', request: {label: 'Cơm tấm', rarity: 3, fps: 24, width: 854, seed: 23}},
];

/**
 * @param {string} name
 * @param {{buffer: Buffer, byteLength: number, durationMs: number}} result
 */
const save = async (name, result) => {
  const output = path.join(fixturesDir, name);
  await writeFile(output, result.buffer);
  console.log(`${name}: ${output} ${result.byteLength} bytes ${result.durationMs}ms`);
};

for (const fixture of smoke ? fixtures.slice(0, 1) : fixtures) {
  await save(`${fixture.name}.gif`, await renderWheelGif(fixture.request, {timeoutInMilliseconds: 30000}));
}

try {
  for (const fixture of smoke ? gachaFixtures.slice(0, 1) : gachaFixtures) {
    await save(`${fixture.name}.mp4`, await renderGachaVideo(fixture.request, {timeoutInMilliseconds: 60000}));
  }
} finally {
  await closeGachaBrowser();
}
