import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// Build output is embedded into the Go `outpost` binary (internal/dashboard/static)
// and served by the dashboard server at 127.0.0.1:9722.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: '/',
  build: {
    outDir: '../internal/dashboard/static',
    emptyOutDir: true,
  },
  server: {
    // `npm run dev` proxies the Go JSON API + WebSocket to the running daemon.
    port: 6009,
    proxy: {
      '/api': 'http://127.0.0.1:9722',
      '/qr.png': 'http://127.0.0.1:9722',
      '/term/ws': { target: 'ws://127.0.0.1:9722', ws: true },
    },
  },
});
