/**
 * hosts.ts write-queue regression tests.
 *
 * The storage backend is mocked with an ASYNC delay so that, without the
 * single-flight write queue, two concurrent read-modify-write mutations would
 * interleave and clobber each other (the multi-host "collapse to one" bug).
 * With the queue, every mutation is serialized and all survive.
 */
jest.mock('../ws', () => {
  const mem: Record<string, string> = {};
  const delay = () => new Promise((r) => setTimeout(r, 3));
  return {
    __mem: mem,
    storageGet: async (k: string) => {
      await delay();
      return mem[k] ?? null;
    },
    storageSet: async (k: string, v: string) => {
      await delay();
      mem[k] = v;
    },
  };
});

import * as ws from '../ws';
import {
  getActiveHostId,
  type Host,
  listHosts,
  removeHost,
  saveHost,
  setActiveHostId,
  updateHostTokens,
} from '../hosts';

const mk = (id: string): Host => ({ id, name: id, url: 'ws://x:8722', access: 'a', refresh: 'r' });

beforeEach(() => {
  const m = (ws as unknown as { __mem: Record<string, string> }).__mem;
  for (const k of Object.keys(m)) delete m[k];
});

test('concurrent saveHost keeps every distinct host (no clobber)', async () => {
  await Promise.all([saveHost(mk('A')), saveHost(mk('B')), saveHost(mk('C'))]);
  const ids = (await listHosts()).map((h) => h.id).sort();
  expect(ids).toEqual(['A', 'B', 'C']);
});

test('saveHost updates in place (keyed by id), not duplicate', async () => {
  await saveHost(mk('A'));
  await saveHost({ ...mk('A'), name: 'renamed' });
  const hosts = await listHosts();
  expect(hosts).toHaveLength(1);
  expect(hosts[0].name).toBe('renamed');
});

test('removeHost is not resurrected by a concurrent token refresh', async () => {
  await saveHost(mk('A'));
  await Promise.all([removeHost('A'), updateHostTokens('A', 'a2', 'r2')]);
  expect((await listHosts()).find((h) => h.id === 'A')).toBeUndefined();
});

test('removeHost clears the active id when it was active', async () => {
  await saveHost(mk('A'));
  await setActiveHostId('A');
  await removeHost('A');
  expect(await getActiveHostId()).toBeNull();
});

test('updateHostTokens only touches the targeted host', async () => {
  await Promise.all([saveHost(mk('A')), saveHost(mk('B'))]);
  await updateHostTokens('B', 'newAccess', 'newRefresh');
  const hosts = await listHosts();
  expect(hosts.find((h) => h.id === 'A')!.access).toBe('a');
  expect(hosts.find((h) => h.id === 'B')!.access).toBe('newAccess');
});
