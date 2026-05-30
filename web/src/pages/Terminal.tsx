import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import '@xterm/xterm/css/xterm.css';
import { useEffect, useRef } from 'react';

// Full-screen in-browser terminal. Connects to the daemon's /term/ws bridge,
// forwarding the page's query string (?id= or ?tool=&name=).
export function TerminalPage() {
  const hostRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;

    const term = new Terminal({
      theme: { background: '#0d0d0d', foreground: '#e0e0e0', cursor: '#4fc3f7' },
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      fontSize: 14,
      cursorBlink: true,
      scrollback: 5000,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();

    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const ws = new WebSocket(`${proto}://${location.host}/term/ws${location.search}`);
    ws.binaryType = 'arraybuffer';

    const enc = (s: string) => btoa(unescape(encodeURIComponent(s)));
    const send = (o: unknown) => ws.readyState === 1 && ws.send(JSON.stringify(o));

    ws.onopen = () => {
      send({ t: 'r', c: term.cols, r: term.rows });
      term.focus();
    };
    ws.onmessage = (e: MessageEvent) => {
      if (typeof e.data === 'string') term.write(e.data);
      else term.write(new Uint8Array(e.data as ArrayBuffer));
    };
    ws.onclose = () => term.write('\r\n\x1b[90m[disconnected]\x1b[0m\r\n');

    const dataSub = term.onData((d) => send({ t: 'i', d: enc(d) }));
    const onResize = () => {
      fit.fit();
      send({ t: 'r', c: term.cols, r: term.rows });
    };
    window.addEventListener('resize', onResize);

    return () => {
      window.removeEventListener('resize', onResize);
      dataSub.dispose();
      ws.close();
      term.dispose();
    };
  }, []);

  return <div ref={hostRef} style={{ height: '100vh', width: '100vw', background: '#0d0d0d' }} />;
}
