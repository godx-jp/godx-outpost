/**
 * useConn — reactive connection/auth state for screens.
 *
 * Screens must not read wsClient.isAuthed directly at render time: it's a plain
 * value, so when auth completes (or a host switch happens) the screen wouldn't
 * re-render and would stay stuck on "No host connected". These hooks subscribe
 * to the client's change events and return state that triggers a re-render.
 */
import { useEffect, useState } from 'react';
import { wsClient } from './ws';

/** True while a host is connected AND authenticated. Re-renders on change. */
export function useAuthed(): boolean {
  const [authed, setAuthed] = useState(wsClient.isAuthed);
  useEffect(() => {
    const update = () => setAuthed(wsClient.isAuthed);
    update(); // sync immediately in case it changed before subscribing
    return wsClient.addChangeListener(update);
  }, []);
  return authed;
}
