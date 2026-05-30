/**
 * HostInfo — read-only summary of the active host and the app.
 */
import Constants from 'expo-constants';
import React, { useEffect, useState } from 'react';
import { StyleSheet, View } from 'react-native';
import { Appbar, Divider, List } from 'react-native-paper';
import { getHost, type Host } from './hosts';
import { useAuthed } from './useConn';
import { wsClient } from './ws';

export function HostInfo({ onBack }: { onBack: () => void }) {
  const authed = useAuthed();
  const [host, setHost] = useState<Host | undefined>();

  useEffect(() => {
    const id = wsClient.activeHostId;
    if (id) void getHost(id).then(setHost);
  }, []);

  const version = Constants.expoConfig?.version ?? '—';

  return (
    <View style={styles.flex}>
      <Appbar.Header mode="small">
        <Appbar.BackAction onPress={onBack} />
        <Appbar.Content title="App / Host info" />
      </Appbar.Header>
      <List.Section>
        <List.Item title="Host" description={wsClient.activeHostName ?? '—'} />
        <Divider />
        <List.Item title="URL" description={host?.url ?? '—'} descriptionStyle={styles.mono} />
        <Divider />
        <List.Item title="Device ID" description={wsClient.activeHostId ?? '—'} descriptionStyle={styles.mono} />
        <Divider />
        <List.Item title="Status" description={authed ? 'Connected' : 'Disconnected'} />
        <Divider />
        <List.Item title="App version" description={version} />
      </List.Section>
    </View>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  mono: { fontFamily: 'monospace', fontSize: 13 },
});
