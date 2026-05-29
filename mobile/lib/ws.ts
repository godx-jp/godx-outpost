/**
 * ws.ts — WebSocket client for the remote-host hostd server.
 *
 * Features:
 *   - Auth flow: pair(code) → auth(accessToken) → refresh on expiry
 *   - Typed send/receive via Envelope and BinaryFrame (protocol.ts)
 *   - Auto-reconnect with exponential back-off
 *   - Token persistence via expo-secure-store (imported lazily so the module
 *     can be unit-tested outside an Expo environment)
 */

import {
  type BinaryFrame,
  type Channel,
  type Envelope,
  Ch,
  decodeBinaryFrame,
  encodeBinaryFrame,
} from './protocol';

// ---------------------------------------------------------------------------
// Token storage (lazy Expo import)
// ---------------------------------------------------------------------------

interface KVStore {
  getItemAsync(key: string): Promise<string | null>;
  setItemAsync(key: string, value: string): Promise<void>;
  deleteItemAsync(key: string): Promise<void>;
}

let _store: KVStore | null = null;

// fallbackStore persists via localStorage when available (web) and otherwise
// keeps values in memory (last resort). Used when expo-secure-store is not
// available — notably on web, where it isn't supported.
function fallbackStore(): KVStore {
  const ls =
    typeof globalThis !== 'undefined' && (globalThis as { localStorage?: Storage }).localStorage
      ? (globalThis as { localStorage: Storage }).localStorage
      : null;
  if (ls) {
    return {
      getItemAsync: async (k) => ls.getItem(k),
      setItemAsync: async (k, v) => ls.setItem(k, v),
      deleteItemAsync: async (k) => ls.removeItem(k),
    };
  }
  const mem = new Map<string, string>();
  return {
    getItemAsync: async (k) => mem.get(k) ?? null,
    setItemAsync: async (k, v) => void mem.set(k, v),
    deleteItemAsync: async (k) => void mem.delete(k),
  };
}

async function secureStore(): Promise<KVStore> {
  if (_store) return _store;
  try {
    const mod = (await import('expo-secure-store')) as unknown as KVStore & {
      isAvailableAsync?: () => Promise<boolean>;
    };
    const available = mod.isAvailableAsync ? await mod.isAvailableAsync() : true;
    if (available && typeof mod.getItemAsync === 'function') {
      _store = mod;
      return _store;
    }
  } catch {
    /* fall through to fallback */
  }
  _store = fallbackStore();
  return _store;
}

/** Persisted convenience storage (host URL, last device) — same backend as tokens. */
export async function storageGet(key: string): Promise<string | null> {
  try {
    return await (await secureStore()).getItemAsync(key);
  } catch {
    return null;
  }
}

export async function storageSet(key: string, value: string): Promise<void> {
  try {
    await (await secureStore()).setItemAsync(key, value);
  } catch {
    /* best-effort */
  }
}

// Tokens are NOT persisted globally here — the app supports multiple hosts, so
// each host's tokens are stored per-host by lib/hosts.ts. The Client holds the
// *active* host's tokens in memory and reports changes via onTokens so the
// caller can persist them against the right host.

// ---------------------------------------------------------------------------
// Back-off
// ---------------------------------------------------------------------------

const BACKOFF_BASE_MS = 500;
const BACKOFF_MAX_MS  = 30_000;
const BACKOFF_FACTOR  = 2;

function nextBackoff(current: number): number {
  return Math.min(current * BACKOFF_FACTOR, BACKOFF_MAX_MS);
}

// ---------------------------------------------------------------------------
// URL helper
// ---------------------------------------------------------------------------

/**
 * hostd serves the WebSocket at the /ws path. Users (and QR payloads) often
 * give just ws://host:port, so append /ws when no path is present. Without this
 * the socket connects to "/" — which hostd doesn't route — and fails with a
 * bare "connection error".
 */
export function normalizeWsUrl(url: string): string {
  try {
    const u = new URL(url);
    if (u.pathname === '' || u.pathname === '/') u.pathname = '/ws';
    return u.toString();
  } catch {
    return /\/ws\/?$/.test(url) ? url : url.replace(/\/+$/, '') + '/ws';
  }
}

// ---------------------------------------------------------------------------
// Callback types
// ---------------------------------------------------------------------------

export type EnvelopeCallback = (env: Envelope) => void;
export type BinaryCallback   = (frame: BinaryFrame) => void;
export type StatusCallback   = (status: ClientStatus, err?: Error) => void;

export type ClientStatus =
  | 'disconnected'
  | 'connecting'
  | 'authenticating'
  | 'connected'
  | 'reconnecting';

// ---------------------------------------------------------------------------
// Auth envelope data shapes (ctrl channel)
// ---------------------------------------------------------------------------

// NOTE: these field names mirror the Go server's ctrl payloads exactly
// (internal/server/server.go). Do not rename without changing the server.
interface PairRequest     { code: string }
interface PairResponse    { access: string; refresh: string; deviceId: string } // ctrl "paired"
interface AuthRequest     { access: string }
interface AuthResponse    { deviceId: string }                  // ctrl "ok"
interface RefreshRequest  { refresh: string }
interface RefreshResponse { access: string }                    // ctrl "refreshed" (access only)

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

export class Client {
  // Public callbacks — set before calling connect().
  onEnvelope: EnvelopeCallback | null = null;
  onBinary:   BinaryCallback   | null = null;
  onStatus:   StatusCallback   | null = null;
  /** Fired whenever the active host's tokens change (pair/refresh), so the
   *  caller can persist them against the right host. */
  onTokens: ((access: string, refresh: string) => void) | null = null;

  // Active host's credentials, held in memory (persistence is per-host, in
  // lib/hosts.ts). Seed via setTokens() before connect()/resume().
  private _access:  string | null = null;
  private _refresh: string | null = null;

  private url:    string | null = null;
  private ws:     WebSocket | null = null;
  private status: ClientStatus = 'disconnected';

  /** Pending response promises keyed by envelope ID. */
  private pending = new Map<string, {
    resolve: (env: Envelope) => void;
    reject:  (err: Error)    => void;
  }>();

  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private backoffMs  = BACKOFF_BASE_MS;
  private stopped    = false;
  private _idCounter = 0;
  private _authed    = false;

  // ---------------------------------------------------------------------------
  // Public API
  // ---------------------------------------------------------------------------

  /**
   * Connect to the hostd WebSocket endpoint and run the auth flow.
   * If tokens exist in secure-store they are used automatically.
   * When no tokens exist the socket opens but the caller must call pair()
   * before sending application messages.
   * Resolves once the socket is open and (if tokens existed) authenticated.
   */
  async connect(url: string): Promise<void> {
    this.stopped = false;
    this.url     = normalizeWsUrl(url);
    return this._openAndAuth();
  }

  /** Seed the active host's tokens (from the host registry) before connecting. */
  setTokens(access: string | null, refresh: string | null): void {
    this._access = access;
    this._refresh = refresh;
  }

  /** Clear in-memory tokens (e.g. when switching hosts or on hard auth failure). */
  clearTokens(): void {
    this._access = null;
    this._refresh = null;
    this._authed = false;
  }

  /**
   * Resume a session: connect to url and authenticate with the in-memory
   * access/refresh tokens (seed them via setTokens first). Returns true if we
   * ended up authenticated, false if there was no usable token. Never throws.
   */
  async resume(url: string): Promise<boolean> {
    if (!this._access && !this._refresh) return false;
    try {
      await this.connect(url);
      return this._authed;
    } catch {
      return false;
    }
  }

  /** Disconnect permanently (suppresses auto-reconnect). */
  disconnect(): void {
    this.stopped = true;
    this._authed = false;
    this._clearReconnect();
    this.ws?.close(1000, 'client disconnect');
    this.ws = null;
    this._setStatus('disconnected');
  }

  /**
   * Pair with a new host using a one-time pairing code. Sets the active
   * credentials in memory, reports them via onTokens, and returns the server's
   * deviceId so the caller can save this host. The socket is authenticated.
   */
  async pair(code: string): Promise<{ access: string; refresh: string; deviceId: string }> {
    const res = await this._request<PairRequest, PairResponse>(
      Ch.Ctrl, 'pair', { code },
    );
    this._access = res.access;
    this._refresh = res.refresh;
    this._authed = true;
    this.onTokens?.(res.access, res.refresh);
    return res;
  }

  /**
   * Authenticate with an existing access token (ctrl/auth).
   * Called automatically by connect(); exposed for manual use.
   */
  async auth(accessToken: string): Promise<void> {
    await this._request<AuthRequest, AuthResponse>(
      Ch.Ctrl, 'auth', { access: accessToken },
    );
  }

  /**
   * Exchange a refresh token for a new access/refresh pair (ctrl/refresh).
   * Persists the updated tokens in secure-store.
   */
  async refresh(refreshToken: string): Promise<void> {
    const res = await this._request<RefreshRequest, RefreshResponse>(
      Ch.Ctrl, 'refresh', { refresh: refreshToken },
    );
    // The server's "refresh" returns only a new access token; the refresh
    // token is unchanged. Update memory and report so the caller re-persists.
    this._access = res.access;
    this._refresh = refreshToken;
    this.onTokens?.(res.access, refreshToken);
  }

  /**
   * Send a JSON envelope over the WebSocket text frame.
   * Throws if the socket is not open.
   */
  send(env: Envelope): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      throw new Error('ws: not connected');
    }
    this.ws.send(JSON.stringify(env));
  }

  /**
   * Send a binary frame (terminal input, file bytes) over a WebSocket binary
   * frame using the BinaryFrame wire format from protocol.ts.
   * Throws if the socket is not open.
   */
  sendBinary(frame: BinaryFrame): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      throw new Error('ws: not connected');
    }
    this.ws.send(encodeBinaryFrame(frame));
  }

  // ---------------------------------------------------------------------------
  // Internal: open + auth sequence
  // ---------------------------------------------------------------------------

  private async _openAndAuth(): Promise<void> {
    this._setStatus('connecting');
    await this._openSocket();

    this._setStatus('authenticating');
    const access = this._access;
    const refresh = this._refresh;

    if (access) {
      try {
        await this.auth(access);
        this._authed = true;
      } catch {
        if (refresh) {
          // refresh() updates the in-memory access token; server now trusts us.
          await this.refresh(refresh);
          this._authed = true;
        } else {
          this.clearTokens();
          throw new Error('ws: auth failed and no refresh token available');
        }
      }
    }
    // No tokens → caller must pair() before sending application messages.

    this._setStatus('connected');
    this.backoffMs = BACKOFF_BASE_MS; // reset on successful connect
  }

  // ---------------------------------------------------------------------------
  // Internal: WebSocket lifecycle
  // ---------------------------------------------------------------------------

  private _openSocket(): Promise<void> {
    return new Promise((resolve, reject) => {
      const ws   = new WebSocket(this.url!);
      this.ws    = ws;
      ws.binaryType = 'arraybuffer';

      ws.onopen = () => resolve();

      ws.onerror = () => {
        reject(new Error('ws: connection error'));
        // onclose fires next; reconnect logic lives there.
      };

      ws.onclose = (evt) => {
        this._handleClose(evt.code, evt.reason);
      };

      ws.onmessage = (evt) => {
        this._handleMessage(evt.data);
      };
    });
  }

  private _handleClose(code: number, reason: string): void {
    this.ws = null;
    this._authed = false;
    const err = new Error(`ws: closed (code=${code} reason=${reason || 'none'})`);
    for (const p of this.pending.values()) p.reject(err);
    this.pending.clear();

    if (this.stopped) return;

    this._setStatus('reconnecting');
    this._scheduleReconnect();
  }

  private _scheduleReconnect(): void {
    this._clearReconnect();
    const delay = this.backoffMs;
    this.reconnectTimer = setTimeout(async () => {
      try {
        await this._openAndAuth();
      } catch {
        this.backoffMs = nextBackoff(this.backoffMs);
        this._scheduleReconnect();
      }
    }, delay);
  }

  private _clearReconnect(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  // ---------------------------------------------------------------------------
  // Internal: message dispatch
  // ---------------------------------------------------------------------------

  private _handleMessage(data: string | ArrayBuffer): void {
    if (typeof data === 'string') {
      let env: Envelope;
      try {
        env = JSON.parse(data) as Envelope;
      } catch {
        console.warn('ws: received invalid JSON text frame');
        return;
      }

      // Resolve a pending request/response pair if the ID matches.
      if (env.id && this.pending.has(env.id)) {
        const p = this.pending.get(env.id)!;
        this.pending.delete(env.id);
        if (env.err) {
          p.reject(new Error(env.err));
        } else {
          p.resolve(env);
        }
        // Do NOT forward to onEnvelope — this is a private r/r message.
        return;
      }

      this.onEnvelope?.(env);
    } else {
      let frame: BinaryFrame;
      try {
        frame = decodeBinaryFrame(data);
      } catch (e) {
        console.warn('ws: received invalid binary frame', e);
        return;
      }
      this.onBinary?.(frame);
    }
  }

  // ---------------------------------------------------------------------------
  // Internal: request/response over text frames
  // ---------------------------------------------------------------------------

  private _request<TReq, TRes>(
    ch:   Channel,
    type: string,
    data: TReq,
  ): Promise<TRes> {
    return new Promise<TRes>((resolve, reject) => {
      const id = this._nextID();

      this.pending.set(id, {
        resolve: (env: Envelope) => resolve(env.data as TRes),
        reject,
      });

      try {
        this.send({ ch, type, id, data });
      } catch (e) {
        this.pending.delete(id);
        reject(e);
      }
    });
  }

  private _nextID(): string {
    return `${Date.now()}-${++this._idCounter}`;
  }

  // ---------------------------------------------------------------------------
  // Internal: status helper
  // ---------------------------------------------------------------------------

  private _setStatus(s: ClientStatus, err?: Error): void {
    this.status = s;
    this.onStatus?.(s, err);
  }

  // ---------------------------------------------------------------------------
  // Accessors
  // ---------------------------------------------------------------------------

  get currentStatus(): ClientStatus {
    return this.status;
  }

  get isConnected(): boolean {
    return this.status === 'connected';
  }

  /** True once pairing or token auth has succeeded on the current connection. */
  get isAuthed(): boolean {
    return this._authed;
  }
}

/** Singleton convenience export — import and call connect() once at app start. */
export const wsClient = new Client();
