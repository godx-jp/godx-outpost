/**
 * protocol.ts — TypeScript mirror of internal/protocol/protocol.go
 *
 * Wire format:
 *   Text  frames → JSON Envelope
 *   Binary frames → BinaryFrame (1 byte kind | 2-byte big-endian idLen | id | payload)
 */

// ---------------------------------------------------------------------------
// Channel
// ---------------------------------------------------------------------------

/** Identifies which subsystem an Envelope targets. */
export type Channel = 'ctrl' | 'term' | 'fs' | 'sys' | 'api';

export const Ch = {
  Ctrl: 'ctrl' as Channel, // pairing, auth, ping/pong, session lifecycle
  Term: 'term' as Channel, // PTY terminal
  FS:   'fs'   as Channel, // file operations
  Sys:  'sys'  as Channel, // system metrics + process control
  API:  'api'  as Channel, // custom user-registered handlers
} as const;

// ---------------------------------------------------------------------------
// Envelope (text frames)
// ---------------------------------------------------------------------------

/**
 * JSON envelope sent over text WebSocket frames.
 * ID correlates a response with its request; omit for streaming/event messages.
 */
export interface Envelope {
  ch:    Channel;
  type:  string;
  id?:   string;
  /** Arbitrary JSON payload — keep as unknown and narrow at call sites. */
  data?: unknown;
  err?:  string;
}

// ---------------------------------------------------------------------------
// BinaryKind
// ---------------------------------------------------------------------------

/** Tags a binary frame so the receiver can route it. Matches Go BinaryKind. */
export enum BinaryKind {
  /** PTY output — StreamID = terminal session id */
  TermOutput = 1,
  /** PTY input  — StreamID = terminal session id */
  TermInput  = 2,
  /** File bytes — StreamID = transfer id */
  FSData     = 3,
}

// ---------------------------------------------------------------------------
// BinaryFrame helpers
// ---------------------------------------------------------------------------

export interface BinaryFrame {
  kind:     BinaryKind;
  streamID: string;
  payload:  Uint8Array;
}

const te = new TextEncoder();
const td = new TextDecoder();

/**
 * Encode a BinaryFrame to bytes for a WebSocket binary message.
 *
 * Layout (matches BinaryFrame.Encode in protocol.go):
 *   byte  0        : Kind
 *   bytes 1..2     : StreamID length (uint16, big-endian)
 *   bytes 3..3+n   : StreamID (UTF-8)
 *   remaining      : Payload
 */
export function encodeBinaryFrame(frame: BinaryFrame): Uint8Array {
  const idBytes = te.encode(frame.streamID);
  const idLen   = idBytes.byteLength;
  const buf     = new Uint8Array(3 + idLen + frame.payload.byteLength);
  const view    = new DataView(buf.buffer);

  buf[0] = frame.kind;
  view.setUint16(1, idLen, false /* big-endian */);
  buf.set(idBytes, 3);
  buf.set(frame.payload, 3 + idLen);

  return buf;
}

/**
 * Decode bytes from a WebSocket binary message into a BinaryFrame.
 * Throws if the frame is truncated (mirrors ErrShortFrame in protocol.go).
 */
export function decodeBinaryFrame(buf: ArrayBuffer | Uint8Array): BinaryFrame {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
  if (bytes.byteLength < 3) {
    throw new Error('protocol: short binary frame');
  }

  const view  = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const kind  = bytes[0] as BinaryKind;
  const idLen = view.getUint16(1, false /* big-endian */);

  if (bytes.byteLength < 3 + idLen) {
    throw new Error('protocol: short binary frame');
  }

  const streamID = td.decode(bytes.subarray(3, 3 + idLen));
  const payload  = bytes.slice(3 + idLen); // copy, like Go's append([]byte(nil), ...)

  return { kind, streamID, payload };
}
