/**
 * Terminal screen – tmux-like multiple persistent sessions.
 *
 * Two views:
 *   - "list": shows sessions from the host (term/list); create, attach, or kill.
 *   - "term": an xterm.js WebView attached to one session.
 *
 * Sessions live on the host independent of this connection, so attaching
 * replays recent output (scrollback) and detaching/leaving keeps them running.
 *
 * Bridge (postMessage):
 *   WebView -> RN: { type:'ready' } | { type:'term/input', data:b64 } | { type:'term/resize', cols, rows }
 *   RN -> WebView: window.__termWrite(b64)
 *
 * A rotate button in the terminal toolbar forces landscape (more columns) and
 * back, via expo-screen-orientation. UI uses react-native-paper; colour comes
 * from the theme and StyleSheet holds layout only.
 */

import { useFocusEffect } from 'expo-router';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ActivityIndicator, Alert, FlatList, StyleSheet, View } from 'react-native';
import { Appbar, Button, Divider, IconButton, List, Menu, Text } from 'react-native-paper';
import { WebView, WebViewMessageEvent } from 'react-native-webview';
import { BinaryKind, Ch, type Envelope } from '../lib/protocol';
import { TMUX_NEW_CMD, takeTermLaunch, tmuxAttachCmd } from '../lib/termLaunch';
import { TermToolbar } from '../lib/TermToolbar';
import { useAuthed } from '../lib/useConn';
import { wsClient } from '../lib/ws';

// Encode a string to UTF-8 bytes (TextEncoder isn't guaranteed in RN's engine).
function utf8Bytes(s: string): Uint8Array {
  const bin = unescape(encodeURIComponent(s));
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

// Single-quote a path for a POSIX shell so spaces/special chars survive being
// typed at the prompt. A literal single quote is closed, escaped, and reopened.
function shellQuote(p: string): string {
  return `'${p.replace(/'/g, `'\\''`)}'`;
}

interface SessionInfo {
  id: string; title: string; cols: number; rows: number; alive: boolean; created: number;
}

interface TmuxInfo {
  name: string; windows: number; attached: boolean;
}

const TERMINAL_HTML = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0" />
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css" />
  <style>
    html, body { margin: 0; padding: 0; background: #0d0d0d; height: 100%; overflow: hidden; }
    #terminal  { height: 100vh; }
    /* Give the scrollback viewport native momentum scrolling on iOS WKWebView. */
    .xterm-viewport { -webkit-overflow-scrolling: touch; }
  </style>
</head>
<body>
  <div id="terminal"></div>
  <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/xterm-addon-webgl@0.16.0/lib/xterm-addon-webgl.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/xterm-addon-canvas@0.5.0/lib/xterm-addon-canvas.js"></script>
  <script>
    const term = new Terminal({
      theme: { background: '#0d0d0d', foreground: '#e0e0e0', cursor: '#4fc3f7' },
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      fontSize: 14, cursorBlink: true,
      scrollback: 5000,          // more history to scroll through
      smoothScrollDuration: 0,   // instant scroll — avoids a laggy feel
    });
    const fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(document.getElementById('terminal'));

    // Use a GPU/Canvas renderer instead of the default DOM renderer — this is
    // the biggest smoothness win. Prefer WebGL; fall back to Canvas, then to the
    // DOM renderer if neither loads (e.g. no GL context in this WebView).
    try {
      const gl = new WebglAddon.WebglAddon();
      gl.onContextLoss(() => { try { gl.dispose(); } catch (_) {} });
      term.loadAddon(gl);
    } catch (_) {
      try { term.loadAddon(new CanvasAddon.CanvasAddon()); } catch (__) {}
    }

    fitAddon.fit();

    function post(obj) { window.ReactNativeWebView.postMessage(JSON.stringify(obj)); }
    function postResize() { post({ type: 'term/resize', cols: term.cols, rows: term.rows }); }

    window.addEventListener('resize', () => { fitAddon.fit(); postResize(); });

    term.onData((data) => {
      const bytes = new TextEncoder().encode(data);
      let bin = '';
      bytes.forEach(b => bin += String.fromCharCode(b));
      post({ type: 'term/input', data: btoa(bin) });
    });

    // RN -> WebView: write base64 output bytes.
    window.__termWrite = function (b64) {
      try {
        const bin = atob(b64);
        const bytes = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        term.write(bytes);
      } catch (_) {}
    };

    // Tell RN we're ready so it can attach (and the initial size).
    postResize();
    post({ type: 'ready' });
  </script>
</body>
</html>`;

export default function TerminalScreen() {
  const [view, setView]           = useState<'list' | 'term'>('list');
  const [sessions, setSessions]   = useState<SessionInfo[]>([]);
  const [tmux, setTmux]           = useState<{ available: boolean; sessions: TmuxInfo[] }>({ available: false, sessions: [] });
  const [newMenu, setNewMenu]     = useState(false);
  const [activeId, setActiveId]   = useState<string | null>(null);
  const [creating, setCreating]   = useState(false);
  const [landscape, setLandscape] = useState(false);
  const [uploading, setUploading] = useState(false);
  const authed = useAuthed();

  const webviewRef = useRef<WebView>(null);
  const activeIdRef = useRef<string | null>(null);
  activeIdRef.current = activeId;
  const creatingRef = useRef(false);
  creatingRef.current = creating;
  const createTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastSizeRef = useRef<{ cols: number; rows: number }>({ cols: 80, rows: 24 });
  // Output coalescing: terminal output arrives as many small binary frames.
  // Injecting each one across the RN↔WebView bridge separately is the main
  // source of scroll/render jank under heavy output. Instead we accumulate
  // payloads and flush them in ONE injectJavaScript per animation frame.
  const outChunksRef = useRef<Uint8Array[]>([]);
  const outFlushRef = useRef<number | null>(null);
  // Host this screen last showed sessions for; used to detect a host switch.
  const lastHostRef = useRef<string | null>(wsClient.activeHostId);
  // Command to run in the session currently being launched (Files long-press),
  // moved to initCmds keyed by sessionId once "created" returns.
  const launchCmdRef = useRef<string | null>(null);
  const initCmdsRef  = useRef<Record<string, string>>({});
  // Lets the focus effect call createSession, which is declared further down.
  const createSessionRef = useRef<(() => void) | null>(null);

  const refreshList = useCallback(() => {
    if (!wsClient.isConnected) return;
    wsClient.send({ ch: Ch.Term, type: 'list' });
    wsClient.send({ ch: Ch.Term, type: 'list-tmux' });
  }, []);

  // Toggle forced landscape (wider terminal) vs free orientation. expo-screen-
  // orientation is a NATIVE module: lazy-import it and ignore failures so this
  // screen still loads in builds that don't include it (otherwise the whole
  // Terminal route fails to register and the tab disappears).
  const toggleOrientation = useCallback(async () => {
    try {
      const SO = await import('expo-screen-orientation');
      if (landscape) {
        await SO.unlockAsync();
        setLandscape(false);
      } else {
        await SO.lockAsync(SO.OrientationLock.LANDSCAPE);
        setLandscape(true);
      }
    } catch {
      /* native module not in this build — rotate is a no-op */
    }
  }, [landscape]);

  const resetOrientation = useCallback(() => {
    setLandscape(false);
    import('expo-screen-orientation').then((SO) => SO.unlockAsync()).catch(() => {});
  }, []);

  // Sessions are per-host. When the tab regains focus, if the active host
  // changed (user switched on the Hosts tab), drop the old host's session
  // state and re-list for the new host. Leaving the tab resets orientation.
  useFocusEffect(
    useCallback(() => {
      if (wsClient.activeHostId !== lastHostRef.current) {
        lastHostRef.current = wsClient.activeHostId;
        setView('list');
        setActiveId(null);
        setSessions([]);
      }
      refreshList();
      // A Files long-press may have queued a "launch here" intent: open a fresh
      // session and run the command in it once attached.
      const launch = takeTermLaunch();
      if (launch && wsClient.isAuthed) {
        launchCmdRef.current = launch.cmd;
        createSessionRef.current?.();
      }
      return resetOrientation;
    }, [refreshList, resetOrientation]),
  );

  useEffect(() => {
    if (!authed) return;

    const prevEnv = wsClient.onEnvelope;
    wsClient.onEnvelope = (env: Envelope) => {
      prevEnv?.(env);
      if (env.ch !== Ch.Term) return;
      const d = (env.data ?? {}) as any;
      switch (env.type) {
        case 'list':
          setSessions((d.sessions ?? []) as SessionInfo[]);
          break;
        case 'list-tmux':
          setTmux({ available: !!d.available, sessions: (d.sessions ?? []) as TmuxInfo[] });
          break;
        case 'created':
          // New session created on host → open it.
          if (createTimer.current) clearTimeout(createTimer.current);
          setCreating(false);
          // If this session was launched from Files, remember the command to
          // type once its WebView attaches.
          if (launchCmdRef.current) {
            initCmdsRef.current[d.sessionId] = launchCmdRef.current;
            launchCmdRef.current = null;
          }
          setActiveId(d.sessionId);
          setView('term');
          break;
        case 'killed':
        case 'exit':
          if (activeIdRef.current === d.sessionId) {
            setActiveId(null);
            setView('list');
          }
          refreshList();
          break;
      }
    };

    // Flush all pending output payloads in ONE bridge crossing: concatenate the
    // queued chunks, base64-encode once, and inject a single __termWrite. Called
    // on an animation frame so a burst of frames coalesces into one DOM update.
    const flushOut = () => {
      outFlushRef.current = null;
      const chunks = outChunksRef.current;
      if (chunks.length === 0) return;
      outChunksRef.current = [];

      let total = 0;
      for (const c of chunks) total += c.length;
      const merged = new Uint8Array(total);
      let off = 0;
      for (const c of chunks) { merged.set(c, off); off += c.length; }

      // Build the binary string in 32KiB slices so String.fromCharCode.apply
      // doesn't blow the argument/stack limit on large bursts.
      let bin = '';
      for (let i = 0; i < merged.length; i += 0x8000) {
        bin += String.fromCharCode.apply(null, Array.from(merged.subarray(i, i + 0x8000)));
      }
      const b64 = btoa(bin);
      // Guard __termWrite: a frame can arrive in the brief window before the
      // WebView has loaded xterm.js and defined the function. Skipping it then
      // is safe — attach() replays scrollback once the WebView is ready.
      webviewRef.current?.injectJavaScript(
        `if(window.__termWrite){window.__termWrite(${JSON.stringify(b64)});}true;`
      );
    };

    const prevBin = wsClient.onBinary;
    wsClient.onBinary = (frame) => {
      prevBin?.(frame);
      if (frame.kind !== BinaryKind.TermOutput) return;
      if (frame.streamID !== activeIdRef.current) return;
      // Queue the payload; flush once per animation frame (RN gives a fresh
      // buffer per WS message, so holding the reference until flush is safe).
      outChunksRef.current.push(frame.payload);
      if (outFlushRef.current == null) {
        outFlushRef.current = requestAnimationFrame(flushOut);
      }
    };

    refreshList();

    return () => {
      wsClient.onEnvelope = prevEnv;
      wsClient.onBinary = prevBin;
      if (outFlushRef.current != null) {
        cancelAnimationFrame(outFlushRef.current);
        outFlushRef.current = null;
      }
      outChunksRef.current = [];
    };
  }, [authed, refreshList]);

  const createSession = () => {
    if (!wsClient.isAuthed) {
      wsClient.onError?.('Not connected to a host — open the Hosts tab and connect first.');
      return;
    }
    setCreating(true);
    try {
      wsClient.send({ ch: Ch.Term, type: 'create', data: { cols: 80, rows: 24 } });
    } catch (e) {
      setCreating(false);
      wsClient.onError?.(`Could not create session: ${(e as Error).message}`);
      return;
    }
    // Feedback if the host never answers (no silent hang).
    if (createTimer.current) clearTimeout(createTimer.current);
    createTimer.current = setTimeout(() => {
      if (creatingRef.current) {
        setCreating(false);
        wsClient.onError?.('No response from host when creating a session (timed out).');
      }
    }, 6000);
    // activeId/view set when "created" arrives.
  };
  createSessionRef.current = createSession;

  // Create a fresh hostd session and, once attached, run a command in it
  // (reuses the launch-command injection used by Files long-press).
  const createWithCmd = (cmd: string) => {
    launchCmdRef.current = cmd;
    createSession();
  };

  const openSession = (id: string) => {
    setActiveId(id);
    setView('term');
    // attach is sent once the WebView signals 'ready'.
  };

  const killSession = (id: string) => {
    wsClient.send({ ch: Ch.Term, type: 'kill', data: { sessionId: id } });
  };

  const leaveToList = () => {
    if (activeIdRef.current) {
      wsClient.send({ ch: Ch.Term, type: 'detach', data: { sessionId: activeIdRef.current } });
    }
    resetOrientation();
    setActiveId(null);
    setView('list');
    refreshList();
  };

  // Toolbar key → send raw bytes to the session, then refocus xterm's input so
  // the on-screen keyboard stays open.
  const sendKeys = useCallback((bytes: number[]) => {
    const id = activeIdRef.current;
    if (!id) return;
    wsClient.sendBinary({ kind: BinaryKind.TermInput, streamID: id, payload: new Uint8Array(bytes) });
    webviewRef.current?.injectJavaScript(
      "var t=document.querySelector('.xterm-helper-textarea'); if(t)t.focus(); true;",
    );
  }, []);

  // Upload a base64 blob to the host, then type its returned host path into the
  // active session (shell-quoted, no Enter) so the user can finish the command —
  // e.g. reference an image with a tool running in the terminal. The PTY runs on
  // the host, so the returned path is directly usable there.
  const uploadAndPaste = useCallback(async (name: string, base64: string) => {
    const id = activeIdRef.current;
    if (!id || !base64) return;
    setUploading(true);
    try {
      const path = await wsClient.fsUpload(name, base64);
      sendKeys(Array.from(utf8Bytes(`${shellQuote(path)} `)));
    } catch (e) {
      wsClient.onError?.(`Upload failed: ${(e as Error).message}`);
    } finally {
      setUploading(false);
    }
  }, [sendKeys]);

  // Attach button → pick an image from the library or any file, read its bytes
  // as base64, then upload+paste. Pickers are NATIVE modules: lazy-import and
  // surface failures rather than crashing the screen on builds without them.
  const pickImage = useCallback(async () => {
    try {
      const ImagePicker = await import('expo-image-picker');
      const res = await ImagePicker.launchImageLibraryAsync({
        mediaTypes: ImagePicker.MediaTypeOptions.Images,
        quality: 1,
        base64: true,
      });
      if (res.canceled || !res.assets?.length) return;
      const a = res.assets[0];
      if (!a.base64) { wsClient.onError?.('Could not read image bytes.'); return; }
      await uploadAndPaste(a.fileName ?? 'image.jpg', a.base64);
    } catch (e) {
      wsClient.onError?.(`Image picker unavailable: ${(e as Error).message}`);
    }
  }, [uploadAndPaste]);

  const pickFile = useCallback(async () => {
    try {
      const DocumentPicker = await import('expo-document-picker');
      const FileSystem = await import('expo-file-system');
      const res = await DocumentPicker.getDocumentAsync({ copyToCacheDirectory: true });
      if (res.canceled || !res.assets?.length) return;
      const a = res.assets[0];
      const base64 = await FileSystem.readAsStringAsync(a.uri, {
        encoding: FileSystem.EncodingType.Base64,
      });
      await uploadAndPaste(a.name ?? 'file', base64);
    } catch (e) {
      wsClient.onError?.(`File picker unavailable: ${(e as Error).message}`);
    }
  }, [uploadAndPaste]);

  const onAttach = useCallback(() => {
    if (uploading) return;
    Alert.alert('Attach to terminal', 'Upload to the host and paste its path.', [
      { text: 'Photo / Image', onPress: pickImage },
      { text: 'File', onPress: pickFile },
      { text: 'Cancel', style: 'cancel' },
    ]);
  }, [uploading, pickImage, pickFile]);

  const handleWebViewMessage = (event: WebViewMessageEvent) => {
    let msg: { type: string; data?: string; cols?: number; rows?: number };
    try { msg = JSON.parse(event.nativeEvent.data); } catch { return; }
    const id = activeIdRef.current;
    if (!id) return;

    if (msg.type === 'ready') {
      // Attach now that xterm exists; scrollback replays into it.
      wsClient.send({
        ch: Ch.Term, type: 'attach',
        data: { sessionId: id, cols: lastSizeRef.current.cols, rows: lastSizeRef.current.rows },
      });
      // If launched from Files, type the queued command once the shell settles.
      const init = initCmdsRef.current[id];
      if (init) {
        delete initCmdsRef.current[id];
        setTimeout(() => {
          wsClient.sendBinary({
            kind: BinaryKind.TermInput, streamID: id, payload: utf8Bytes(`${init}\r`),
          });
        }, 450);
      }
    } else if (msg.type === 'term/input' && msg.data) {
      const bin = atob(msg.data);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      wsClient.sendBinary({ kind: BinaryKind.TermInput, streamID: id, payload: bytes });
    } else if (msg.type === 'term/resize' && msg.cols && msg.rows) {
      lastSizeRef.current = { cols: msg.cols, rows: msg.rows };
      wsClient.send({ ch: Ch.Term, type: 'resize', data: { sessionId: id, cols: msg.cols, rows: msg.rows } });
    }
  };

  if (!authed) {
    return (
      <View style={styles.center}>
        <Text variant="bodyLarge">No host connected. Go to the Hosts tab and connect.</Text>
      </View>
    );
  }

  // ── Terminal view ──────────────────────────────────────────────────────────
  if (view === 'term' && activeId) {
    return (
      <View style={styles.flex}>
        <Appbar.Header mode="small">
          <Appbar.BackAction onPress={leaveToList} />
          <Appbar.Content title={activeId} titleStyle={styles.mono} />
          {uploading ? (
            <ActivityIndicator style={styles.uploadSpin} color="#4fc3f7" />
          ) : (
            <Appbar.Action icon="paperclip" onPress={onAttach} />
          )}
          <Appbar.Action
            icon={landscape ? 'phone-rotate-portrait' : 'phone-rotate-landscape'}
            onPress={toggleOrientation}
          />
          <Appbar.Action icon="trash-can-outline" onPress={() => killSession(activeId)} />
        </Appbar.Header>
        <WebView
          key={activeId}            /* fresh xterm per session */
          ref={webviewRef}
          source={{ html: TERMINAL_HTML }}
          style={styles.webview}
          onMessage={handleWebViewMessage}
          originWhitelist={['*']}
          keyboardDisplayRequiresUserAction={false}
          automaticallyAdjustContentInsets={false}
          contentInsetAdjustmentBehavior="never"
          decelerationRate="normal"
          overScrollMode="never"
        />
        <TermToolbar onKey={sendKeys} />
      </View>
    );
  }

  // ── Session list view ───────────────────────────────────────────────────────
  return (
    <View style={styles.flex}>
      <Appbar.Header mode="small">
        <Appbar.Content title="Sessions" subtitle={wsClient.activeHostName ?? 'host'} />
        <Appbar.Action icon="refresh" onPress={refreshList} />
      </Appbar.Header>

      {tmux.available ? (
        <Menu
          visible={newMenu}
          onDismiss={() => setNewMenu(false)}
          anchor={
            <Button
              mode="contained"
              icon="plus"
              onPress={() => setNewMenu(true)}
              loading={creating}
              disabled={creating}
              style={styles.newBtn}
            >
              {creating ? 'Creating…' : 'New Session'}
            </Button>
          }
        >
          <Menu.Item
            leadingIcon="console-line"
            title="Shell session"
            onPress={() => { setNewMenu(false); createSession(); }}
          />
          <Menu.Item
            leadingIcon="view-grid-outline"
            title="tmux session"
            onPress={() => { setNewMenu(false); createWithCmd(TMUX_NEW_CMD); }}
          />
        </Menu>
      ) : (
        <Button
          mode="contained"
          icon="plus"
          onPress={createSession}
          loading={creating}
          disabled={creating}
          style={styles.newBtn}
        >
          {creating ? 'Creating…' : 'New Session'}
        </Button>
      )}

      <FlatList
        data={[
          ...tmux.sessions.map((t) => ({ kind: 'tmux' as const, t })),
          ...sessions.map((s) => ({ kind: 'host' as const, s })),
        ]}
        keyExtractor={(row) => (row.kind === 'tmux' ? `tmux:${row.t.name}` : `host:${row.s.id}`)}
        ListEmptyComponent={
          <Text variant="bodyMedium" style={styles.empty}>No sessions yet. Tap “New Session”.</Text>
        }
        ItemSeparatorComponent={Divider}
        renderItem={({ item }) =>
          item.kind === 'tmux' ? (
            <List.Item
              title={item.t.name}
              description={`tmux · ${item.t.windows} window${item.t.windows === 1 ? '' : 's'}${item.t.attached ? ' · attached' : ''}`}
              onPress={() => createWithCmd(tmuxAttachCmd(item.t.name))}
              left={(props) => <List.Icon {...props} icon="view-grid-outline" />}
            />
          ) : (
            <List.Item
              title={item.s.title}
              description={`${item.s.id} · ${item.s.cols}×${item.s.rows}${item.s.alive ? '' : ' · exited'}`}
              descriptionStyle={styles.mono}
              onPress={() => openSession(item.s.id)}
              left={(props) => <List.Icon {...props} icon="console-line" />}
              right={(props) => (
                <IconButton {...props} icon="trash-can-outline" onPress={() => killSession(item.s.id)} />
              )}
            />
          )
        }
      />
    </View>
  );
}

const styles = StyleSheet.create({
  flex:    { flex: 1 },
  webview: { flex: 1, backgroundColor: '#0d0d0d' },
  center:  { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24 },
  mono:    { fontFamily: 'monospace', fontSize: 13 },
  newBtn: { margin: 16 },
  empty:  { textAlign: 'center', marginTop: 40, paddingHorizontal: 24 },
  uploadSpin: { marginHorizontal: 14 },
});
