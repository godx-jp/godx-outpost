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
 */

import { useFocusEffect } from 'expo-router';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  FlatList, StyleSheet, Text, TouchableOpacity, View,
} from 'react-native';
import { WebView, WebViewMessageEvent } from 'react-native-webview';
import { BinaryKind, Ch, type Envelope } from '../lib/protocol';
import { wsClient } from '../lib/ws';

interface SessionInfo {
  id: string; title: string; cols: number; rows: number; alive: boolean; created: number;
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
  </style>
</head>
<body>
  <div id="terminal"></div>
  <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
  <script>
    const term = new Terminal({
      theme: { background: '#0d0d0d', foreground: '#e0e0e0', cursor: '#4fc3f7' },
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      fontSize: 14, cursorBlink: true,
    });
    const fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(document.getElementById('terminal'));
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
  const [view, setView]         = useState<'list' | 'term'>('list');
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const webviewRef = useRef<WebView>(null);
  const activeIdRef = useRef<string | null>(null);
  activeIdRef.current = activeId;
  const creatingRef = useRef(false);
  creatingRef.current = creating;
  const createTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastSizeRef = useRef<{ cols: number; rows: number }>({ cols: 80, rows: 24 });
  // Host this screen last showed sessions for; used to detect a host switch.
  const lastHostRef = useRef<string | null>(wsClient.activeHostId);

  const refreshList = useCallback(() => {
    if (wsClient.isConnected) wsClient.send({ ch: Ch.Term, type: 'list' });
  }, []);

  // Sessions are per-host. When the tab regains focus, if the active host
  // changed (user switched on the Hosts tab), drop the old host's session
  // state and re-list for the new host.
  useFocusEffect(
    useCallback(() => {
      if (wsClient.activeHostId !== lastHostRef.current) {
        lastHostRef.current = wsClient.activeHostId;
        setView('list');
        setActiveId(null);
        setSessions([]);
      }
      refreshList();
    }, [refreshList]),
  );

  useEffect(() => {
    if (!wsClient.isConnected) return;

    const prevEnv = wsClient.onEnvelope;
    wsClient.onEnvelope = (env: Envelope) => {
      prevEnv?.(env);
      if (env.ch !== Ch.Term) return;
      const d = (env.data ?? {}) as any;
      switch (env.type) {
        case 'list':
          setSessions((d.sessions ?? []) as SessionInfo[]);
          break;
        case 'created':
          // New session created on host → open it.
          if (createTimer.current) clearTimeout(createTimer.current);
          setCreating(false);
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

    const prevBin = wsClient.onBinary;
    wsClient.onBinary = (frame) => {
      prevBin?.(frame);
      if (frame.kind !== BinaryKind.TermOutput) return;
      if (frame.streamID !== activeIdRef.current) return;
      let bin = '';
      frame.payload.forEach((b) => (bin += String.fromCharCode(b)));
      const b64 = btoa(bin);
      // Guard __termWrite: a frame can arrive in the brief window before the
      // WebView has loaded xterm.js and defined the function. Skipping it then
      // is safe — attach() replays scrollback once the WebView is ready.
      webviewRef.current?.injectJavaScript(
        `if(window.__termWrite){window.__termWrite(${JSON.stringify(b64)});}true;`
      );
    };

    refreshList();

    return () => {
      wsClient.onEnvelope = prevEnv;
      wsClient.onBinary = prevBin;
    };
  }, [refreshList]);

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
    setActiveId(null);
    setView('list');
    refreshList();
  };

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

  if (!wsClient.isAuthed) {
    return (
      <View style={styles.center}>
        <Text style={styles.notice}>No host connected. Go to the Hosts tab and connect.</Text>
      </View>
    );
  }

  // ── Terminal view ──────────────────────────────────────────────────────────
  if (view === 'term' && activeId) {
    return (
      <View style={styles.container}>
        <View style={styles.termHeader}>
          <TouchableOpacity onPress={leaveToList}>
            <Text style={styles.headerBtn}>‹ Sessions</Text>
          </TouchableOpacity>
          <Text style={styles.headerTitle} numberOfLines={1}>{activeId}</Text>
          <TouchableOpacity onPress={() => killSession(activeId)}>
            <Text style={[styles.headerBtn, styles.killBtn]}>Kill</Text>
          </TouchableOpacity>
        </View>
        <WebView
          key={activeId}            /* fresh xterm per session */
          ref={webviewRef}
          source={{ html: TERMINAL_HTML }}
          style={styles.webview}
          onMessage={handleWebViewMessage}
          originWhitelist={['*']}
          keyboardDisplayRequiresUserAction={false}
        />
      </View>
    );
  }

  // ── Session list view ───────────────────────────────────────────────────────
  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <View style={{ flex: 1 }}>
          <Text style={styles.heading}>Sessions</Text>
          <Text style={styles.hostSub} numberOfLines={1}>
            {wsClient.activeHostName ?? 'host'}
          </Text>
        </View>
        <TouchableOpacity onPress={refreshList}>
          <Text style={styles.headerBtn}>Refresh</Text>
        </TouchableOpacity>
      </View>

      <TouchableOpacity
        style={[styles.newBtn, creating && styles.newBtnBusy]}
        onPress={createSession}
        disabled={creating}
      >
        <Text style={styles.newBtnText}>{creating ? 'Creating…' : '+ New Session'}</Text>
      </TouchableOpacity>

      <FlatList
        data={sessions}
        keyExtractor={(s) => s.id}
        ListEmptyComponent={<Text style={styles.empty}>No sessions yet. Tap “New Session”.</Text>}
        renderItem={({ item }) => (
          <TouchableOpacity style={styles.row} onPress={() => openSession(item.id)}>
            <View style={{ flex: 1 }}>
              <Text style={styles.rowTitle}>{item.title}</Text>
              <Text style={styles.rowSub}>{item.id} · {item.cols}×{item.rows}{item.alive ? '' : ' · exited'}</Text>
            </View>
            <TouchableOpacity onPress={() => killSession(item.id)} hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}>
              <Text style={[styles.headerBtn, styles.killBtn]}>Kill</Text>
            </TouchableOpacity>
          </TouchableOpacity>
        )}
        ItemSeparatorComponent={() => <View style={styles.separator} />}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  container:   { flex: 1, backgroundColor: '#0d0d0d' },
  webview:     { flex: 1, backgroundColor: '#0d0d0d' },
  center:      { flex: 1, backgroundColor: '#0d0d0d', alignItems: 'center', justifyContent: 'center', padding: 24 },
  notice:      { color: '#aaaaaa', fontSize: 15, textAlign: 'center' },
  header:      { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', padding: 16, borderBottomWidth: 1, borderBottomColor: '#222' },
  termHeader:  { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', padding: 12, backgroundColor: '#111', borderBottomWidth: 1, borderBottomColor: '#222' },
  heading:     { color: '#e0e0e0', fontSize: 20, fontWeight: '700' },
  hostSub:     { color: '#4fc3f7', fontSize: 12, fontFamily: 'monospace', marginTop: 2 },
  headerBtn:   { color: '#4fc3f7', fontSize: 15 },
  headerTitle: { color: '#888', fontSize: 13, fontFamily: 'monospace', flex: 1, textAlign: 'center' },
  killBtn:     { color: '#ef5350' },
  newBtn:      { margin: 16, padding: 14, borderRadius: 8, borderWidth: 1, borderColor: '#4fc3f7', alignItems: 'center' },
  newBtnBusy:  { opacity: 0.5 },
  newBtnText:  { color: '#4fc3f7', fontSize: 16, fontWeight: '600' },
  empty:       { color: '#555', textAlign: 'center', marginTop: 24, fontSize: 14 },
  row:         { flexDirection: 'row', alignItems: 'center', paddingHorizontal: 16, paddingVertical: 14 },
  rowTitle:    { color: '#e0e0e0', fontSize: 16 },
  rowSub:      { color: '#666', fontSize: 12, fontFamily: 'monospace', marginTop: 2 },
  separator:   { height: 1, backgroundColor: '#1a1a1a', marginLeft: 16 },
});
