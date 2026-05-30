/**
 * Cross-screen "launch a terminal here" intent.
 *
 * The Files screen sets an intent (a shell command to run in a fresh session),
 * then navigates to the Terminal tab, which consumes it: it creates a new
 * session and, once the WebView is attached, types the command into the shell.
 *
 * The backend `term create` takes no cwd/command, so the directory change and
 * tool launch are expressed as a single shell command injected as keystrokes.
 */

export interface TermLaunch {
  /** Full shell command to run once the new session is ready. */
  cmd: string;
  /** Human label, shown briefly if needed. */
  label?: string;
}

let pending: TermLaunch | null = null;

export function setTermLaunch(l: TermLaunch): void {
  pending = l;
}

/** Returns the pending intent (once) and clears it. */
export function takeTermLaunch(): TermLaunch | null {
  const p = pending;
  pending = null;
  return p;
}

export type LaunchKind = 'term' | 'claude' | 'claude-yolo' | 'codex' | 'codex-yolo';

/** Single-quote a string for POSIX shells, escaping embedded quotes. */
function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

/**
 * Build a `cd` command that survives spaces while still letting the shell
 * expand a leading `~` (which would not expand inside quotes).
 */
export function cdCommand(path: string): string {
  if (path === '~') return 'cd ~';
  if (path.startsWith('~/')) return `cd ~/${shellQuote(path.slice(2))}`;
  return `cd ${shellQuote(path)}`;
}

/** Build the full shell command for a long-press launch option. */
export function buildLaunchCmd(path: string, kind: LaunchKind): string {
  const cd = cdCommand(path);
  switch (kind) {
    case 'term':        return cd;
    case 'claude':      return `${cd} && claude`;
    case 'claude-yolo': return `${cd} && claude --dangerously-skip-permissions`;
    case 'codex':       return `${cd} && codex`;
    case 'codex-yolo':  return `${cd} && codex --dangerously-bypass-approvals-and-sandbox`;
  }
}
