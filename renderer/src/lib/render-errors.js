export class RenderTimeoutError extends Error {
  /**
   * @param {number} timeoutMs
   * @param {ErrorOptions} [options]
   */
  constructor(timeoutMs, options) {
    super(`Render exceeded ${timeoutMs}ms`, options);
    this.name = 'RenderTimeoutError';
    this.timeoutMs = timeoutMs;
  }
}

/**
 * @param {unknown} error
 * @returns {error is RenderTimeoutError}
 */
export const isRenderTimeoutError = (error) => error instanceof RenderTimeoutError;
