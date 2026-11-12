import {randomInt} from 'node:crypto';
import {ZodError} from 'zod';
import {isRenderTimeoutError} from '../lib/render-errors.js';
import {maxGachaSeed, parseGachaRequest} from '../schemas/gacha-request.js';

/**
 * @typedef {import('../config.js').AppConfig} AppConfig
 * @typedef {import('../schemas/gacha-request.js').GachaRenderRequest} GachaRenderRequest
 */

/**
 * @typedef {object} GachaRouteDeps
 * @property {AppConfig} config
 * @property {{tryAcquire: () => boolean, release: () => void}} semaphore
 * @property {(request: GachaRenderRequest, options: {timeoutInMilliseconds: number}) => Promise<{buffer: Buffer, durationMs: number, byteLength: number}>} renderGacha
 */

/**
 * The caller picks the result and its rarity; this route only draws the wish
 * animation for it.
 *
 * @param {import('fastify').FastifyInstance} app
 * @param {GachaRouteDeps} deps
 */
export const registerGachaRoute = async (app, deps) => {
  app.post('/api/gacha', async (request, reply) => {
    let gachaRequest;
    try {
      gachaRequest = parseGachaRequest(request.body, {maxOptionChars: deps.config.maxOptionChars}, () =>
        randomInt(maxGachaSeed),
      );
    } catch (error) {
      if (error instanceof ZodError) {
        return reply.code(400).send({error: 'invalid_request', issues: error.issues});
      }
      throw error;
    }

    if (!deps.semaphore.tryAcquire()) {
      return reply.code(429).send({error: 'too_many_renders'});
    }

    try {
      const result = await deps.renderGacha(gachaRequest, {
        timeoutInMilliseconds: deps.config.renderTimeoutMs,
      });

      return reply
        .header('Content-Type', 'video/mp4')
        .header('Content-Disposition', 'inline; filename="gacha.mp4"')
        .header('Cache-Control', 'no-store')
        .header('X-Gacha-Rarity', String(gachaRequest.rarity))
        .header('X-Render-Duration-Ms', String(result.durationMs))
        .send(result.buffer);
    } catch (error) {
      if (isRenderTimeoutError(error)) {
        return reply.code(504).send({error: 'render_timeout'});
      }

      throw error;
    } finally {
      deps.semaphore.release();
    }
  });
};
