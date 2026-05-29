/**
 * Custom screen – stub that uses the "api" channel via wsClient.
 *
 * Sends:  { ch: Ch.API, type: 'list' }            – list registered handlers
 * Sends:  { ch: Ch.API, type: 'call', data: { name, args } }
 * Reads:  { ch: Ch.API, type: 'handlers', data: HandlerInfo[] }
 *         { ch: Ch.API, type: 'result',   data: unknown }
 *         { ch: Ch.API, err: string }
 *
 * Full implementation will render each handler as a form with typed inputs
 * derived from a JSON schema returned by the host.
 */

import React, { useEffect, useState } from 'react';
import {
  ActivityIndicator,
  FlatList,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import { Ch, type Envelope } from '../lib/protocol';
import { wsClient } from '../lib/ws';

interface HandlerInfo {
  name:        string;
  description: string;
}

export default function CustomScreen() {
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

  if (!wsClient.isConnected) {
    return (
      <View style={styles.center}>
        <Text style={styles.notice}>Not paired. Go to the Pair tab first.</Text>
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.heading}>Custom Handlers</Text>
        <TouchableOpacity onPress={fetchHandlers}>
          <Text style={styles.refresh}>Refresh</Text>
        </TouchableOpacity>
      </View>

      {error ? <Text style={styles.error}>{error}</Text> : null}
      {lastResult ? (
        <View style={styles.resultBox}>
          <Text style={styles.resultLabel}>Last result</Text>
          <Text style={styles.resultText}>{lastResult}</Text>
        </View>
      ) : null}

      {loading ? (
        <ActivityIndicator style={{ marginTop: 32 }} color="#4fc3f7" />
      ) : handlers.length === 0 ? (
        <Text style={styles.empty}>No custom handlers registered on this host.</Text>
      ) : (
        <FlatList
          data={handlers}
          keyExtractor={(item) => item.name}
          renderItem={({ item }) => (
            <TouchableOpacity style={styles.card} onPress={() => callHandler(item.name)}>
              <Text style={styles.handlerName}>{item.name}</Text>
              {item.description ? (
                <Text style={styles.handlerDesc}>{item.description}</Text>
              ) : null}
              <Text style={styles.callLabel}>Tap to call</Text>
            </TouchableOpacity>
          )}
          contentContainerStyle={{ padding: 16 }}
          ItemSeparatorComponent={() => <View style={{ height: 12 }} />}
        />
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#0d0d0d' },
  center:    {
    flex: 1,
    backgroundColor: '#0d0d0d',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 24,
  },
  notice:      { color: '#aaaaaa', fontSize: 15, textAlign: 'center' },
  header:      {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: 16,
    borderBottomWidth: 1,
    borderBottomColor: '#222222',
  },
  heading:     { color: '#e0e0e0', fontSize: 18, fontWeight: '700' },
  refresh:     { color: '#4fc3f7', fontSize: 14 },
  error:       { color: '#ef5350', padding: 12, fontSize: 13 },
  empty:       { color: '#555555', fontSize: 14, textAlign: 'center', marginTop: 40, paddingHorizontal: 24 },
  resultBox:   {
    margin: 16,
    backgroundColor: '#111111',
    borderRadius: 8,
    padding: 12,
    borderWidth: 1,
    borderColor: '#2a2a2a',
  },
  resultLabel: { color: '#4fc3f7', fontSize: 11, marginBottom: 6, letterSpacing: 0.6 },
  resultText:  { color: '#cccccc', fontSize: 13, fontFamily: 'monospace' },
  card:        {
    backgroundColor: '#111111',
    borderRadius: 10,
    padding: 16,
    borderWidth: 1,
    borderColor: '#222222',
  },
  handlerName: { color: '#e0e0e0', fontSize: 16, fontWeight: '600', marginBottom: 4 },
  handlerDesc: { color: '#888888', fontSize: 13, lineHeight: 18, marginBottom: 8 },
  callLabel:   { color: '#4fc3f7', fontSize: 12 },
});
