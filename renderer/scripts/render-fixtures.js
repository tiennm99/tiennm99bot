import {mkdir, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {closeGachaBrowser, renderGachaVideo} from '../src/render/render-gacha.js';

const smoke = process.argv.includes('--smoke');
const fixturesDir = path.join(process.cwd(), 'fixtures');

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

try {
  for (const fixture of smoke ? gachaFixtures.slice(0, 1) : gachaFixtures) {
    await save(`${fixture.name}.mp4`, await renderGachaVideo(fixture.request, {timeoutInMilliseconds: 60000}));
  }
} finally {
  await closeGachaBrowser();
}
