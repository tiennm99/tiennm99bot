import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {bundle} from '@remotion/bundler';

/** @type {Promise<string> | undefined} */
let bundlePromise;

/**
 * @returns {Promise<string>}
 */
export const getRemotionServeUrl = () => {
  if (!bundlePromise) {
    const entryPoint = fileURLToPath(new URL('../remotion/index.jsx', import.meta.url));
    bundlePromise = bundle({
      entryPoint,
      outDir: path.join(process.cwd(), '.tmp', 'remotion-bundle'),
    });
  }

  return bundlePromise;
};
