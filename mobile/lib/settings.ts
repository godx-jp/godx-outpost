/**
 * settings.ts — small local preferences, stored via the same KV store as hosts.
 */
import { storageGet, storageSet } from './ws';

const DIR_KEY = 'rh_files_default_dir';
export const DEFAULT_FILES_DIR = '~/projects';

export async function getDefaultDir(): Promise<string> {
  return (await storageGet(DIR_KEY)) || DEFAULT_FILES_DIR;
}

export async function setDefaultDir(dir: string): Promise<void> {
  await storageSet(DIR_KEY, dir || DEFAULT_FILES_DIR);
}
