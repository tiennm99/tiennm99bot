import {mkdtemp, rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

/**
 * @returns {Promise<string>}
 */
export const createRenderTempDir = () => mkdtemp(path.join(os.tmpdir(), 'renderer-'));

/**
 * @param {string} dir
 * @returns {Promise<void>}
 */
export const cleanupTempDir = async (dir) => {
  await rm(dir, {force: true, recursive: true});
};
