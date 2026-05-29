/**
 * Terminal screen – embeds xterm.js in a WebView.
 *
 * Bridge protocol (postMessage both ways):
 *   WebView -> RN:  { type: "term/input",  data: <base64 binary> }
 *                   { type: "term/resize", cols: number, rows: number }
 *   RN -> WebView:  { type: "term/output", data: <base64 binary> }
 *
 * RN bridges to wsClient:
 *   user input -> wsClient.sendBinary({ kind: BinaryKind.TermInput, streamID, payload })
 *   output     <- wsClient.onBinary  ({ kind: BinaryKind.TermOutput, streamID, payload })
 *   resize     -> wsClient.send({ ch: Ch.Term, type: 'resize', data: { cols, rows } })
 *   open       -> wsClient.send({ ch: Ch.Term, type: 'open' })
 *   close      -> wsClient.send({ ch: Ch.Term, type: 'close' })
 */

import React, { useEffect, useRef } from 'react';
import { StyleSheet, Text, View } from 'react-native';
import { WebView, WebViewMessageEvent } from 'react-native-webview';
import { BinaryKind, Ch } from '../lib/protocol';
import { wsClient } from '../lib/ws';

// Session ID for this terminal (a real app would get this from the server open response).
const SESSION_ID = 'default';

// Inline HTML that loads xterm.js from CDN and wires up postMessage bridging.
const TERMINAL_HTML = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0" />
  <title>Terminal</title>
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
      fontSize: 14,
      cursorBlink: true,
    });

    const fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(document.getElementById('terminal'));
    fitAddon.fit();

    function postResize() {
      window.ReactNativeWebView.postMessage(JSON.stringify({
        type: 'term/resize', cols: term.cols, rows: term.rows,
      }));
    }
    postResize();

    window.addEventListener('resize', () => { fitAddon.fit(); postResize(); });

    // User typed -> forward to RN as base64
    term.onData((data) => {
      const bytes = new TextEncoder().encode(data);
      let bin = '';
      bytes.forEach(b => bin += String.fromCharCode(b));
      window.ReactNativeWebView.postMessage(JSON.stringify({
        type: 'term/input', data: btoa(bin),
      }));
    });

    // RN -> WebView: render output
    window.addEventListener('message', (event) => {
      try {
        const msg = JSON.parse(event.data);
        if (msg.type === 'term/output') {
          const bin   = atob(msg.data);
          const bytes = new Uint8Array(bin.length);
          for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
          term.write(bytes);
        }
      } catch (_) { /* ignore */ }
    });

    term.writeln('\\x1b[1;34mremote-host\\x1b[0m – waiting for connection…');
  </script>
</body>
</html>`;

export default function TerminalScreen() {
  const webviewRef = useRef<WebView>(null);

  useEffect(() => {
    if (!wsClient.isConnected) return;

    // Open a PTY session on the host
    wsClient.send({ ch: Ch.Term, type: 'open', data: { sessionID: SESSION_ID } });

    // Route binary output from host to the WebView
    const prevOnBinary = wsClient.onBinary;
    wsClient.onBinary = (frame) => {
      prevOnBinary?.(frame);
      if (frame.kind !== BinaryKind.TermOutput || frame.streamID !== SESSION_ID) return;

      let bin = '';
      frame.payload.forEach((b) => (bin += String.fromCharCode(b)));
      const b64 = btoa(bin);

      webviewRef.current?.injectJavaScript(
        `window.dispatchEvent(new MessageEvent('message',{data:${JSON.stringify(
          JSON.stringify({ type: 'term/output', data: b64 })
        )})));void 0;`
      );
    };

    return () => {
      wsClient.onBinary = prevOnBinary;
      wsClient.send({ ch: Ch.Term, type: 'close', data: { sessionID: SESSION_ID } });
    };
  }, []);

  const handleWebViewMessage = (event: WebViewMessageEvent) => {
    try {
      const msg = JSON.parse(event.nativeEvent.data) as {
        type: string;
        data?: string;
        cols?: number;
        rows?: number;
      };

      if (msg.type === 'term/input' && msg.data) {
        const bin   = atob(msg.data);
        const bytes = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        wsClient.sendBinary({ kind: BinaryKind.TermInput, streamID: SESSION_ID, payload: bytes });
      }

      if (msg.type === 'term/resize') {
        wsClient.send({
          ch:   Ch.Term,
          type: 'resize',
          data: { sessionID: SESSION_ID, cols: msg.cols, rows: msg.rows },
        });
      }
    } catch {
      // ignore malformed messages
    }
  };

  if (!wsClient.isConnected) {
    return (
      <View style={styles.center}>
        <Text style={styles.notice}>Not paired. Go to the Pair tab first.</Text>
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <WebView
        ref={webviewRef}
        source={{ html: TERMINAL_HTML }}
        style={styles.webview}
        onMessage={handleWebViewMessage}
        originWhitelist={['*']}
        allowsInlineMediaPlayback
        mediaPlaybackRequiresUserAction={false}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#0d0d0d' },
  webview:   { flex: 1, backgroundColor: '#0d0d0d' },
  center:    {
    flex: 1,
    backgroundColor: '#0d0d0d',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 24,
  },
  notice: { color: '#aaaaaa', fontSize: 15, textAlign: 'center' },
});
