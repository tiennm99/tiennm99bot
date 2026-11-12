import {spawn} from 'node:child_process';
import {mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {ensureBrowser, makeCancelSignal, RenderInternals} from '@remotion/renderer';
import {
  gachaTotalSeconds,
  getDragPointer,
  getWishFrameSize,
  getWishPageProps,
  resolveWishAsset,
  wishOrigin,
} from '../gacha/wish-plan.js';
import {RenderTimeoutError} from '../lib/render-errors.js';
import {cleanupTempDir, createRenderTempDir} from '../lib/tmp-files.js';
import {createCdpConnection} from './cdp-pipe.js';

/**
 * @typedef {object} RenderResult
 * @property {Buffer} buffer
 * @property {number} durationMs
 * @property {number} byteLength
 */

/**
 * @typedef {import('../schemas/gacha-request.js').GachaRenderRequest} GachaRenderRequest
 * @typedef {import('./cdp-pipe.js').CdpConnection} CdpConnection
 * @typedef {{chrome: import('node:child_process').ChildProcess, cdp: CdpConnection}} WishBrowser
 */

const roots = {
  page: fileURLToPath(new URL('../gacha/page', import.meta.url)),
  packCards: path.dirname(fileURLToPath(import.meta.resolve('pack-cards'))),
};

/** Virtual milliseconds the page gets to load and mount its WebGL pack before capture starts. */
const maxLoadMs = 5000;

/**
 * Chrome flags for frame-exact capture: `--deterministic-mode` lets the
 * caller issue every BeginFrame, so animations, timers, and WebGL advance
 * only with virtual time. WebGL runs on SwiftShader because the server has no
 * GPU.
 *
 * @param {string} userDataDir
 */
const chromeArgs = (userDataDir) => [
  '--deterministic-mode',
  '--remote-debugging-pipe',
  '--no-sandbox',
  '--no-first-run',
  '--mute-audio',
  '--hide-scrollbars',
  '--use-angle=swiftshader',
  '--enable-unsafe-swiftshader',
  `--user-data-dir=${userDataDir}`,
  'about:blank',
];

/**
 * Answers every page request from disk and blocks anything else, so the page
 * cannot reach the network.
 *
 * @param {CdpConnection} cdp
 */
const serveWishPages = (cdp) => {
  cdp.on('Fetch.requestPaused', (params, sessionId) => {
    const asset = resolveWishAsset(params.request.url, roots);
    const refuse = () =>
      cdp.send('Fetch.failRequest', {requestId: params.requestId, errorReason: 'BlockedByClient'}, sessionId);
    const answer = asset
      ? readFile(asset.file).then(
          (body) =>
            cdp.send(
              'Fetch.fulfillRequest',
              {
                requestId: params.requestId,
                responseCode: 200,
                responseHeaders: [{name: 'Content-Type', value: asset.contentType}],
                body: body.toString('base64'),
              },
              sessionId,
            ),
          refuse,
        )
      : refuse();
    answer.catch(() => {});
  });
};

/**
 * One headless Chrome per server process. SwiftShader spends several seconds
 * compiling the pack's shaders in the first tab; later tabs in the same
 * browser reuse that work.
 *
 * @type {Promise<WishBrowser> | null}
 */
let sharedBrowser = null;

/** @returns {Promise<WishBrowser>} */
const launchBrowser = async () => {
  const installed = await ensureBrowser({logLevel: 'error'});
  if (!('path' in installed) || !installed.path) {
    throw new Error(`No Chrome available for the gacha wish (${installed.type})`);
  }
  const profileDir = await mkdtemp(path.join(os.tmpdir(), 'renderer-chrome-'));
  const chrome = spawn(installed.path, chromeArgs(profileDir), {stdio: ['ignore', 'ignore', 'ignore', 'pipe', 'pipe']});
  const cdp = createCdpConnection(
    /** @type {NodeJS.WritableStream} */ (chrome.stdio[3]),
    /** @type {NodeJS.ReadableStream} */ (chrome.stdio[4]),
  );
  const killOnExit = () => chrome.kill('SIGKILL');
  process.once('exit', killOnExit);
  chrome.once('exit', () => {
    process.off('exit', killOnExit);
    sharedBrowser = null;
    cdp.fail(new Error('Chrome exited during the gacha wish render'));
    rm(profileDir, {force: true, recursive: true}).catch(() => {});
  });
  serveWishPages(cdp);
  await cdp.send('Browser.getVersion');
  return {chrome, cdp};
};

/** @returns {Promise<WishBrowser>} */
const getBrowser = () => {
  sharedBrowser ??= launchBrowser().catch((error) => {
    sharedBrowser = null;
    throw error;
  });
  return sharedBrowser;
};

/**
 * Stops the shared browser, if one is running. The server calls this on
 * shutdown; scripts call it so their process can exit.
 *
 * @returns {Promise<void>}
 */
export const closeGachaBrowser = async () => {
  const current = sharedBrowser;
  if (!current) {
    return;
  }
  const chrome = await current.then(
    (browser) => browser.chrome,
    () => null,
  );
  if (!chrome || chrome.exitCode !== null) {
    return;
  }
  const exited = new Promise((resolve) => chrome.once('exit', resolve));
  chrome.kill('SIGTERM');
  const forced = setTimeout(() => chrome.kill('SIGKILL'), 2000);
  await exited;
  clearTimeout(forced);
};

/**
 * Pays the shared browser's one-time costs with throwaway wishes, so the first
 * real request renders as fast as later ones: the WebGL shader compilation,
 * and the first raster of each card's engraving texture. 4★ and 5★ cards use
 * different engravings, so both are warmed.
 *
 * @returns {Promise<void>}
 */
export const warmGachaBrowser = async () => {
  for (const rarity of /** @type {const} */ ([5, 4])) {
    await renderGachaVideo({label: 'Warm-up', rarity, fps: 24, width: 640, seed: 1}, {timeoutInMilliseconds: 120000});
  }
};

/**
 * A seeded Math.random so a seed reproduces the same flecks and sparkles.
 *
 * @param {number} seed
 */
const seededRandomScript = (seed) => `(() => {
  let state = ${seed >>> 0};
  Math.random = () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let value = state;
    value = Math.imul(value ^ (value >>> 15), value | 1);
    value ^= value + Math.imul(value ^ (value >>> 7), value | 61);
    return ((value ^ (value >>> 14)) >>> 0) / 4294967296;
  };
})();`;

/**
 * Captures the wish into JPEG frames in `dir`, one per video frame.
 *
 * @param {CdpConnection} cdp
 * @param {string} sessionId
 * @param {GachaRenderRequest} request
 * @param {string} dir
 * @param {<T>(promise: Promise<T>) => Promise<T>} guard  Rejects when the render is abandoned.
 */
const captureFrames = async (cdp, sessionId, request, dir, guard) => {
  const interval = 1000 / request.fps;
  /**
   * @param {string} method
   * @param {Record<string, unknown>} [params]
   */
  const send = (method, params) => guard(cdp.send(method, params, sessionId));
  /** @param {string} expression */
  const evaluate = async (expression) =>
    (await send('Runtime.evaluate', {expression, returnByValue: true})).result?.value;

  await send('Page.enable');
  await send('Fetch.enable', {patterns: [{urlPattern: '*'}]});
  await send('Emulation.setDeviceMetricsOverride', {
    ...getWishFrameSize(request.width),
    deviceScaleFactor: 1,
    mobile: false,
  });
  await send('Page.addScriptToEvaluateOnNewDocument', {
    source: `${seededRandomScript(request.seed)}window.gachaWish = ${JSON.stringify(getWishPageProps(request))};`,
  });
  const {virtualTimeTicksBase} = await send('Emulation.setVirtualTimePolicy', {policy: 'pause'});
  let elapsed = 0;

  /**
   * Advances virtual time by one frame, then draws that frame with a
   * BeginFrame stamped on the same clock so requestAnimationFrame,
   * performance.now(), and CSS animations agree.
   *
   * @param {boolean} capture
   * @returns {Promise<string | undefined>} JPEG data, when captured and changed.
   */
  const step = async (capture) => {
    const expired = cdp.once('Emulation.virtualTimeBudgetExpired', sessionId);
    await send('Emulation.setVirtualTimePolicy', {
      policy: 'pauseIfNetworkFetchesPending',
      budget: interval,
      maxVirtualTimeTaskStarvationCount: 100,
    });
    await guard(expired);
    elapsed += interval;
    const frame = await send('HeadlessExperimental.beginFrame', {
      frameTimeTicks: virtualTimeTicksBase + elapsed,
      interval,
      ...(capture ? {screenshot: {format: 'jpeg', quality: 90}} : {}),
    });
    return frame.screenshotData;
  };

  await send('Page.navigate', {url: `${wishOrigin}/index.html`});
  let box = null;
  for (let waited = 0; !box && waited < maxLoadMs; waited += interval) {
    await step(false);
    box = await evaluate(`(() => {
      const control = document.querySelector('.recap-webgl-pack [role=button]');
      if (!control) return null;
      const r = control.getBoundingClientRect();
      return {x: r.left, y: r.top, w: r.width, h: r.height};
    })()`);
  }
  if (!box) {
    throw new Error('The gacha wish pack did not mount');
  }

  let pressed = false;
  let last = '';
  let pointer = {x: 0, y: 0};
  const frames = Math.round(gachaTotalSeconds * request.fps);
  for (let index = 0; index < frames; index += 1) {
    const drag = getDragPointer(index / request.fps);
    /** @type {Promise<unknown>[]} */
    const inputs = [];
    /** @param {Record<string, unknown>} event */
    const mouse = (event) => inputs.push(send('Input.dispatchMouseEvent', {...pointer, ...event}));
    if (drag) {
      pointer = {x: box.x + box.w * drag.x, y: box.y + box.h * drag.y};
      if (pressed) {
        mouse({type: 'mouseMoved', button: 'left', buttons: 1});
      } else {
        mouse({type: 'mouseMoved'});
        mouse({type: 'mousePressed', button: 'left', clickCount: 1});
        pressed = true;
      }
    } else if (pressed) {
      mouse({type: 'mouseReleased', button: 'left', clickCount: 1});
      pressed = false;
      // Park the pointer in the corner, off the card. Left where the drag ended,
      // it sits over the card once the card lands, and pack-cards tilts a
      // hovered card toward the pointer, nudging the face after the spin stops.
      pointer = {x: 0, y: 0};
      mouse({type: 'mouseMoved'});
    }
    // Input is acknowledged only once a frame consumes it, so draw before
    // awaiting it. BeginFrame omits the screenshot when nothing changed.
    last = (await step(true)) ?? last;
    await Promise.all(inputs);
    await writeFile(path.join(dir, `f${String(index).padStart(4, '0')}.jpg`), Buffer.from(last, 'base64'));
  }
  if ((await evaluate('document.documentElement.dataset.wish')) !== 'revealed') {
    throw new Error('The gacha wish card was not revealed');
  }
};

/**
 * Renders the gacha wish: a pack-cards pack torn open by a scripted drag,
 * captured frame by frame under virtual time and encoded to a silent H.264
 * MP4.
 *
 * @param {GachaRenderRequest} request
 * @param {{timeoutInMilliseconds: number}} options
 * @returns {Promise<RenderResult>}
 */
export const renderGachaVideo = async (request, options) => {
  const startedAt = Date.now();
  const tempDir = await createRenderTempDir();
  const {cancel, cancelSignal} = makeCancelSignal();
  /** @type {(error: Error) => void} */
  let abandon = () => {};
  /** @type {Promise<never>} */
  const abandoned = new Promise((_, reject) => {
    abandon = reject;
  });
  abandoned.catch(() => {});
  /** @type {<T>(promise: Promise<T>) => Promise<T>} */
  const guard = (promise) => Promise.race([promise, abandoned]);
  const timeoutId = setTimeout(() => {
    cancel();
    abandon(new Error('The gacha wish render was abandoned'));
  }, options.timeoutInMilliseconds);
  /** @type {CdpConnection | null} */
  let cdp = null;
  let targetId = '';

  try {
    const browser = await guard(getBrowser());
    cdp = browser.cdp;
    ({targetId} = await guard(cdp.send('Target.createTarget', {url: 'about:blank', enableBeginFrameControl: true})));
    const {sessionId} = await guard(cdp.send('Target.attachToTarget', {targetId, flatten: true}));
    await captureFrames(cdp, sessionId, request, tempDir, guard);

    const outputLocation = path.join(tempDir, 'gacha.mp4');
    // Remotion is pinned to an exact version; its bundled ffmpeg encodes the frames.
    await RenderInternals.callFf({
      bin: 'ffmpeg',
      args: [
        '-y',
        '-framerate',
        String(request.fps),
        '-i',
        path.join(tempDir, 'f%04d.jpg'),
        '-c:v',
        'libx264',
        '-pix_fmt',
        'yuv420p',
        '-crf',
        '23',
        '-movflags',
        '+faststart',
        '-an',
        outputLocation,
      ],
      indent: false,
      logLevel: 'error',
      binariesDirectory: null,
      cancelSignal,
    });
    const buffer = await readFile(outputLocation);
    return {buffer, byteLength: buffer.byteLength, durationMs: Date.now() - startedAt};
  } catch (error) {
    if (Date.now() - startedAt >= options.timeoutInMilliseconds) {
      throw new RenderTimeoutError(options.timeoutInMilliseconds, {cause: error});
    }
    throw error;
  } finally {
    clearTimeout(timeoutId);
    if (cdp && targetId) {
      await cdp.send('Target.closeTarget', {targetId}).catch(() => {});
    }
    await cleanupTempDir(tempDir);
  }
};
