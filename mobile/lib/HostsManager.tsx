/**
 * HostsManager — list saved hosts, connect/switch, add (PairFlow), remove.
 *
 * Two modes:
 *   - "login":    shown by the auth gate when no host is connected. No back
 *                 button; with zero saved hosts it drops straight into PairFlow.
 *   - "embedded": opened from the More menu while connected; has a back button.
 */
import React, { useCallback, useEffect, useState } from 'react';
import { FlatList, StyleSheet, View } from 'react-native';
import {
  ActivityIndicator, Appbar, Divider, HelperText, IconButton, List, Text,
  useTheme,
} from 'react-native-paper';
import {
  getActiveHostId, listHosts, removeHost, setActiveHostId, type Host,
} from './hosts';
import { PairFlow } from './PairFlow';
import { type AppTheme } from './theme';
import { useAuthed } from './useConn';
import { wsClient } from './ws';

export function HostsManager({ mode, onBack }: { mode: 'login' | 'embedded'; onBack?: () => void }) {
  const theme  = useTheme<AppTheme>();
  const authed = useAuthed();
  const [hosts, setHosts]     = useState<Host[]>([]);
  const [activeId, setActiveId] = useState<string | null>(wsClient.activeHostId);
  const [busy, setBusy]       = useState('');
  const [error, setError]     = useState('');
  const [adding, setAdding]   = useState(false);

  const reload = useCallback(async () => {
    setHosts(await listHosts());
    setActiveId(await getActiveHostId());
  }, []);

  useEffect(() => { void reload(); }, [reload]);

  const connectHost = useCallback(async (h: Host) => {
    if (h.id === wsClient.activeHostId && wsClient.isAuthed) { onBack?.(); return; }
    setError('');
    setBusy(`Connecting to ${h.name}…`);
    wsClient.disconnect();
    wsClient.clearTokens();
    wsClient.setTokens(h.access, h.refresh);
    wsClient.activeHostId = h.id;
    wsClient.activeHostName = h.name;
    await setActiveHostId(h.id);
    const ok = await wsClient.resume(h.url);
    setBusy('');
    if (ok) {
      setActiveId(h.id);
      onBack?.();
    } else {
      setError(`Could not authenticate with ${h.name}. It may have been revoked — remove and re-pair.`);
    }
  }, [onBack]);

  const onRemove = useCallback(async (h: Host) => {
    if (h.id === wsClient.activeHostId) {
      wsClient.disconnect();
      wsClient.activeHostId = null;
      wsClient.activeHostName = null;
    }
    await removeHost(h.id);
    await reload();
  }, [reload]);

  if (adding) {
    return (
      <PairFlow
        onDone={() => { setAdding(false); void reload(); onBack?.(); }}
        onBack={() => setAdding(false)}
      />
    );
  }

  // Login with no saved hosts → go straight to pairing.
  if (mode === 'login' && hosts.length === 0) {
    return <PairFlow onDone={() => { /* auth gate switches to tabs */ }} />;
  }

  if (busy) {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" />
        <Text variant="bodyMedium" style={styles.busy}>{busy}</Text>
      </View>
    );
  }

  return (
    <View style={styles.flex}>
      <Appbar.Header mode="small">
        {mode === 'embedded' && onBack ? <Appbar.BackAction onPress={onBack} /> : null}
        <Appbar.Content title={mode === 'login' ? 'Connect a host' : 'Hosts'} />
        <Appbar.Action icon="plus" onPress={() => { setError(''); setAdding(true); }} />
      </Appbar.Header>

      {error ? <HelperText type="error" visible>{error}</HelperText> : null}

      <FlatList
        data={hosts}
        keyExtractor={(h) => h.id}
        ItemSeparatorComponent={Divider}
        ListEmptyComponent={
          <Text variant="bodyMedium" style={styles.empty}>
            No hosts yet. Tap “+” to pair your first host.
          </Text>
        }
        renderItem={({ item }) => {
          const connected = item.id === activeId && authed;
          return (
            <List.Item
              title={item.name}
              description={`${item.url}${connected ? ' · connected' : ''}`}
              descriptionNumberOfLines={1}
              descriptionStyle={styles.mono}
              onPress={() => connectHost(item)}
              left={(props) => (
                <List.Icon
                  {...props}
                  icon={connected ? 'circle' : 'circle-outline'}
                  color={connected ? theme.colors.secondary : theme.colors.onSurfaceVariant}
                />
              )}
              right={() => (
                <IconButton icon="trash-can-outline" iconColor={theme.colors.error} onPress={() => onRemove(item)} />
              )}
            />
          );
        }}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  flex:   { flex: 1 },
  center: { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24 },
  busy:   { marginTop: 16 },
  mono:   { fontFamily: 'monospace' },
  empty:  { textAlign: 'center', marginTop: 40, paddingHorizontal: 24 },
});
