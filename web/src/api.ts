// JSON API client for the outpost dashboard (served by the Go daemon at :9722).

export interface Target {
  label: string;
  url: string;
}

export interface SessionRow {
  id: string;
  title: string;
  cwd: string;
  alive: boolean;
  kind: string; // "shell" | "tmux" | "zellij"
}

export interface DeviceRow {
  clientId: string;
  name: string;
  type: string;
  pairedAt: string;
  lastSeen: string;
  status: string; // "active" | "revoked"
}

export interface State {
  deviceId: string;
  code: string;
  advertise: string;
  domain: string;
  targets: Target[];
  sessions: SessionRow[];
  devices: DeviceRow[];
}

export async function fetchState(): Promise<State> {
  const r = await fetch('/api/state');
  if (!r.ok) throw new Error(`state ${r.status}`);
  return r.json();
}

const q = (s: string) => encodeURIComponent(s);

export const killSession = (kind: string, name: string) =>
  fetch(`/api/kill-session?kind=${q(kind)}&name=${q(name)}`);
export const revokeDevice = (id: string) => fetch(`/api/revoke?id=${q(id)}`);
export const renameDevice = (id: string, name: string) =>
  fetch(`/api/rename?id=${q(id)}&name=${q(name)}`);
export const setDomain = (domain: string) => fetch(`/api/domain?domain=${q(domain)}`);
export const refreshCode = () => fetch('/api/code?new=1');

/** URL of the in-browser terminal for a session row. */
export function termHref(s: SessionRow): string {
  return s.kind === 'shell'
    ? `/term?id=${q(s.id)}`
    : `/term?tool=${q(s.kind)}&name=${q(s.id)}`;
}
