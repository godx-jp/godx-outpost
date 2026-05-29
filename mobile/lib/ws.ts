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

interface SecureStore {
  getItemAsync(key: string): Promise<string | null>;
  setItemAsync(key: string, value: string): Promise<void>;
  deleteItemAsync(key: string): Promise<void>;
}

let _store: SecureStore | null = null;

async function secureStore(): Promise<SecureStore> {
  if (!_store) {
    // Lazy import so this module can be imported in non-Expo environments.
    _store = (await import('expo-secure-store')) as unknown as SecureStore;
  }
  return _store;
}

const TOKEN_KEY_ACCESS  = 'rh_access_token';
const TOKEN_KEY_REFRESH = 'rh_refresh_token';

async function loadTokens(): Promise<{ access: string | null; refresh: string | null }> {
  const store = await secureStore();
  const [access, refresh] = await Promise.all([
    store.getItemAsync(TOKEN_KEY_ACCESS),
    store.getItemAsync(TOKEN_KEY_REFRESH),
  ]);
  return { access, refresh };
}

async function saveTokens(access: string, refresh: string): Promise<void> {
  const store = await secureStore();
  await Promise.all([
    store.setItemAsync(TOKEN_KEY_ACCESS, access),
    store.setItemAsync(TOKEN_KEY_REFRESH, refresh),
  ]);
}

async function clearTokens(): Promise<void> {
  const store = await secureStore();
  await Promise.all([
    store.deleteItemAsync(TOKEN_KEY_ACCESS),
    store.deleteItemAsync(TOKEN_KEY_REFRESH),
  ]);
}

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

interface PairRequest     { code: string }
interface PairResponse    { access_token: string; refresh_token: string }
interface AuthRequest     { access_token: string }
interface AuthResponse    { ok: boolean }
interface RefreshRequest  { refresh_token: string }
interface RefreshResponse { access_token: string; refresh_token: string }

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

export class Client {
  // Public callbacks — set before calling connect().
  onEnvelope: EnvelopeCallback | null = null;
  onBinary:   BinaryCallback   | null = null;
  onStatus:   StatusCallback   | null = null;

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
    this.url     = url;
    return this._openAndAuth();
  }

  /** Disconnect permanently (suppresses auto-reconnect). */
  disconnect(): void {
    this.stopped = true;
    this._clearReconnect();
    this.ws?.close(1000, 'client disconnect');
    this.ws = null;
    this._setStatus('disconnected');
  }

  /**
   * Pair with a new host using a one-time pairing code.
   * Persists the returned tokens in secure-store.
   */
  async pair(code: string): Promise<void> {
    const res = await this._request<PairRequest, PairResponse>(
      Ch.Ctrl, 'pair', { code },
    );
    await saveTokens(res.access_token, res.refresh_token);
  }

  /**
   * Authenticate with an existing access token (ctrl/auth).
   * Called automatically by connect(); exposed for manual use.
   */
  async auth(accessToken: string): Promise<void> {
    await this._request<AuthRequest, AuthResponse>(
      Ch.Ctrl, 'auth', { access_token: accessToken },
    );
  }

  /**
   * Exchange a refresh token for a new access/refresh pair (ctrl/refresh).
   * Persists the updated tokens in secure-store.
   */
  async refresh(refreshToken: string): Promise<void> {
    const res = await this._request<RefreshRequest, RefreshResponse>(
      Ch.Ctrl, 'refresh', { refresh_token: refreshToken },
    );
    await saveTokens(res.access_token, res.refresh_token);
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
    const { access, refresh } = await loadTokens();

    if (access) {
      try {
        await this.auth(access);
      } catch {
        if (refresh) {
          // refresh() saves the new tokens and the server considers us authed.
          await this.refresh(refresh);
        } else {
          await clearTokens();
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
}

/** Singleton convenience export — import and call connect() once at app start. */
export const wsClient = new Client();
