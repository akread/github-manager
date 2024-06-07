import path from 'node:path';
import { access, constants, mkdir } from 'node:fs/promises';
import { URL, fileURLToPath } from 'url';

const PROJECT_ROOT_DIR = path.resolve(
  fileURLToPath(new URL('.', import.meta.url)),
  '..',
);

export async function initializeContext(appDir, extraContext) {
  try {
    await access(appDir, constants.F_OK);
  } catch {
    await mkdir(appDir);
  }

  return {
    ...extraContext,
    appDir,
    projectRootDir: PROJECT_ROOT_DIR,
    plugins: {},
  };
}
