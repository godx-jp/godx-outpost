/**
 * CustomHandlers — list and call the host's registered "api" handlers.
 * (Previously the standalone Custom tab; now a page inside the More menu.)
 */
import React, { useEffect, useState } from 'react';
import { FlatList, StyleSheet, View } from 'react-native';
import { ActivityIndicator, Appbar, Card, HelperText, Text } from 'react-native-paper';
import { Ch, type Envelope } from './protocol';
import { wsClient } from './ws';

interface HandlerInfo {
  name:        string;
  description: string;
}

export function CustomHandlers({ onBack }: { onBack: () => void }) {
  const [handlers, setHandlers] = useState<HandlerInfo[]>([]);
  const [loading, setLoading]   = useState(false);
  const [error, setError]       = useState('');
  const [lastResult, setLastResult] = useState<string>('');

  useEffect(() => {
    if (!wsClient.isConnected) return;

    const prevOnEnvelope = wsClient.onEnvelope;
    wsClient.onEnvelope = (env: Envelope) => {
      prevOnEnvelope?.(env);
      if (env.ch !== Ch.API) return;
      if (env.type === 'handlers') {
        setHandlers((env.data as HandlerInfo[]) ?? []);
        setLoading(false);
      }
      if (env.type === 'result') {
        setLastResult(JSON.stringify(env.data, null, 2));
        setLoading(false);
      }
      if (env.err) {
        setError(env.err);
        setLoading(false);
      }
    };

    fetchHandlers();

    return () => {
      wsClient.onEnvelope = prevOnEnvelope;
    };
  }, []);

  const fetchHandlers = () => {
    if (!wsClient.isConnected) return;
    setLoading(true);
    setError('');
    wsClient.send({ ch: Ch.API, type: 'list' });
  };

  const callHandler = (name: string) => {
    if (!wsClient.isConnected) return;
    setLoading(true);
    setError('');
    setLastResult('');
    wsClient.send({ ch: Ch.API, type: 'call', data: { name, args: {} } });
  };

  return (
    <View style={styles.flex}>
      <Appbar.Header mode="small">
        <Appbar.BackAction onPress={onBack} />
        <Appbar.Content title="Custom Handlers" />
        <Appbar.Action icon="refresh" onPress={fetchHandlers} />
      </Appbar.Header>

      {error ? <HelperText type="error" visible>{error}</HelperText> : null}

      {lastResult ? (
        <Card style={styles.result} mode="contained">
          <Card.Content>
            <Text variant="labelMedium">Last result</Text>
            <Text variant="bodyMedium" style={styles.mono}>{lastResult}</Text>
          </Card.Content>
        </Card>
      ) : null}

      {loading ? (
        <View style={styles.center}>
          <ActivityIndicator />
        </View>
      ) : handlers.length === 0 ? (
        <Text variant="bodyMedium" style={styles.empty}>
          No custom handlers registered on this host.
        </Text>
      ) : (
        <FlatList
          data={handlers}
          keyExtractor={(item) => item.name}
          contentContainerStyle={styles.list}
          renderItem={({ item }) => (
            <Card mode="contained" onPress={() => callHandler(item.name)}>
              <Card.Title
                title={item.name}
                subtitle={item.description || undefined}
                subtitleNumberOfLines={3}
              />
              <Card.Content>
                <Text variant="labelMedium">Tap to call</Text>
              </Card.Content>
            </Card>
          )}
        />
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  flex:   { flex: 1 },
  center: { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24 },
  result: { margin: 16 },
  mono:   { fontFamily: 'monospace' },
  empty:  { textAlign: 'center', marginTop: 40, paddingHorizontal: 24 },
  list:   { padding: 16, gap: 12 },
});
