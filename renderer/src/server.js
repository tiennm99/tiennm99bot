import Fastify from 'fastify';
import {loadConfig} from './config.js';
import {createRenderSemaphore} from './lib/render-semaphore.js';
import {closeGachaBrowser, renderGachaVideo, warmGachaBrowser} from './render/render-gacha.js';
import {registerGachaRoute} from './routes/gacha.js';
import {registerHealthRoute} from './routes/health.js';

/**
 * @typedef {import('./config.js').AppConfig} AppConfig
 * @typedef {import('./schemas/gacha-request.js').GachaRenderRequest} GachaRenderRequest
 */

/**
 * @param {object} [options]
 * @param {AppConfig} [options.config]
 * @param {(request: GachaRenderRequest, renderOptions: {timeoutInMilliseconds: number}) => Promise<{buffer: Buffer, durationMs: number, byteLength: number}>} [options.renderGacha]
 * @param {boolean} [options.warmRenderers]  Start the gacha browser before serving.
 */
export const buildServer = async (options = {}) => {
  const config = options.config ?? loadConfig();
  const warmRenderers = options.warmRenderers ?? process.env.NODE_ENV !== 'test';

  if (warmRenderers) {
    await warmGachaBrowser();
  }

  const app = Fastify({
    bodyLimit: 32 * 1024,
    logger: process.env.NODE_ENV !== 'test',
  });

  app.setErrorHandler(async (error, _request, reply) => {
    app.log.error(error);
    return reply.code(500).send({error: 'internal_error'});
  });

  await registerHealthRoute(app);
  // One semaphore across renders: they share the CPU.
  const semaphore = createRenderSemaphore(config.maxConcurrentRenders);
  await registerGachaRoute(app, {
    config,
    renderGacha: options.renderGacha ?? renderGachaVideo,
    semaphore,
  });
  // The gacha wish keeps one headless Chrome alive between renders.
  app.addHook('onClose', closeGachaBrowser);

  return app;
};

if (import.meta.url === `file://${process.argv[1]}`) {
  const config = loadConfig();
  const app = await buildServer({config});
  await app.listen({host: config.host, port: config.port});
}
