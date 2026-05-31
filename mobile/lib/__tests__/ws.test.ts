/**
 * ws.ts pure-helper tests (normalizeWsUrl). The WebSocket connection itself
 * needs a running server, but the URL normalization is pure and worth pinning:
 * a missing /ws path was a real "connection error" bug.
 */
import { normalizeWsUrl } from '../ws';

test('normalizeWsUrl appends /ws when the path is empty or "/"', () => {
  expect(normalizeWsUrl('ws://host:8722')).toBe('ws://host:8722/ws');
  expect(normalizeWsUrl('ws://host:8722/')).toBe('ws://host:8722/ws');
  expect(normalizeWsUrl('wss://outpost.example.com')).toBe('wss://outpost.example.com/ws');
});

test('normalizeWsUrl leaves an existing /ws path untouched', () => {
  expect(normalizeWsUrl('ws://host:8722/ws')).toBe('ws://host:8722/ws');
  expect(normalizeWsUrl('wss://h:1/ws')).toBe('wss://h:1/ws');
});
