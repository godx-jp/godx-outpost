/**
 * hosts.ts — persistent registry of paired hosts.
 *
 * The app can pair with many hosts (like Termius). Each host keeps its own URL
 * and access/refresh tokens; one host is "active" at a time (the connection the
 * Terminal/Files/Monitor tabs use). Persisted via the same KV store as before
 * (SecureStore on native, localStorage on web).
 */

import { storageGet, storageSet } from './ws';

export interface Host {
  id: string;       // server deviceId — stable identity, survives URL changes
  name: string;     // user-facing label
  url: string;      // ws URL (e.g. ws://127.0.0.1:8722)
  access: string;
  refresh: string;
}

const HOSTS_KEY  = 'rh_hosts';
const ACTIVE_KEY = 'rh_active_host';

export async function listHosts(): Promise<Host[]> {
  const raw = await storageGet(HOSTS_KEY);
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? (parsed as Host[]) : [];
  } catch {
    return [];
  }
}

export async function getHost(id: string): Promise<Host | undefined> {
  return (await listHosts()).find((h) => h.id === id);
}

/** Insert or update a host (keyed by id). */
export async function saveHost(host: Host): Promise<void> {
  const hosts = await listHosts();
  const i = hosts.findIndex((h) => h.id === host.id);
  if (i >= 0) hosts[i] = host;
  else hosts.push(host);
  await storageSet(HOSTS_KEY, JSON.stringify(hosts));
}

export async function removeHost(id: string): Promise<void> {
  const hosts = (await listHosts()).filter((h) => h.id !== id);
  await storageSet(HOSTS_KEY, JSON.stringify(hosts));
  if ((await getActiveHostId()) === id) await setActiveHostId(null);
}

/** Update just the tokens for a host (called when they refresh). */
export async function updateHostTokens(id: string, access: string, refresh: string): Promise<void> {
  const h = await getHost(id);
  if (h) await saveHost({ ...h, access, refresh });
}

export async function getActiveHostId(): Promise<string | null> {
  const v = await storageGet(ACTIVE_KEY);
  return v ? v : null;
}

export async function setActiveHostId(id: string | null): Promise<void> {
  await storageSet(ACTIVE_KEY, id ?? '');
}

/** A friendly default name derived from a ws URL (host:port). */
export function defaultHostName(url: string): string {
  try {
    const u = new URL(url);
    return u.host || url;
  } catch {
    return url;
  }
}
