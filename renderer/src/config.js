export const minRenderTimeoutMs = 7000;

/**
 * @typedef {object} AppConfig
 * @property {string} host
 * @property {number} port
 * @property {number} maxConcurrentRenders
 * @property {number} renderTimeoutMs
 * @property {number} maxOptionChars
 */

/**
 * @param {string | undefined} value
 * @param {number} fallback
 * @returns {number}
 */
const parsePositiveInt = (value, fallback) => {
  if (!value) {
    return fallback;
  }

  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
};

/**
 * @param {NodeJS.ProcessEnv} [env]
 * @returns {AppConfig}
 */
export const loadConfig = (env = process.env) => {
  return {
    host: env.RENDERER_HOST || '0.0.0.0',
    port: parsePositiveInt(env.RENDERER_PORT, 3000),
    maxConcurrentRenders: parsePositiveInt(env.RENDERER_MAX_CONCURRENT_RENDERS, 1),
    renderTimeoutMs: Math.max(
      minRenderTimeoutMs,
      parsePositiveInt(env.RENDERER_RENDER_TIMEOUT_MS, 30000),
    ),
    maxOptionChars: parsePositiveInt(env.RENDERER_MAX_OPTION_CHARS, 40),
  };
};
