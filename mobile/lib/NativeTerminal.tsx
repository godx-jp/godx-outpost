/**
 * NativeTerminal — thin JS wrapper around the native SwiftTerm-backed view
 * (iOS only). The native module renders the terminal, owns the keyboard (with
 * full IME, so Vietnamese Telex works) and native scrolling. We feed output
 * bytes in and receive typed bytes out.
 *
 * Bridge:
 *   <RemoteTerminalView onReady onData onSizeChange />  — the view
 *   feedTerminal(tag, base64)  — write output bytes from the WebSocket
 *   focusTerminal(tag)         — keep the keyboard up after toolbar taps
 */
import {
  NativeModules,
  type NativeSyntheticEvent,
  requireNativeComponent,
  type ViewProps,
} from 'react-native';

export type TermDataEvent = NativeSyntheticEvent<{ base64: string }>;
export type TermSizeEvent = NativeSyntheticEvent<{ cols: number; rows: number }>;

interface NativeTerminalProps extends ViewProps {
  onData?: (e: TermDataEvent) => void;
  onSizeChange?: (e: TermSizeEvent) => void;
  onReady?: (e: NativeSyntheticEvent<Record<string, never>>) => void;
}

// Manager class name minus "Manager" → JS component name "RemoteTerminalView".
export const RemoteTerminalView =
  requireNativeComponent<NativeTerminalProps>('RemoteTerminalView');

const Manager = NativeModules.RemoteTerminalViewManager as
  | { feed(reactTag: number, base64: string): void; focus(reactTag: number): void }
  | undefined;

/** Write base64-encoded output bytes into the terminal (no-op if not mounted). */
export function feedTerminal(reactTag: number | null, base64: string): void {
  if (reactTag == null || !Manager) return;
  Manager.feed(reactTag, base64);
}

/** Refocus the terminal so the on-screen keyboard stays open. */
export function focusTerminal(reactTag: number | null): void {
  if (reactTag == null || !Manager) return;
  Manager.focus(reactTag);
}
