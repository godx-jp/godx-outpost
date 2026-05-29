/**
 * Files screen – stub that uses the "fs" channel via wsClient.
 *
 * Sends:  { ch: Ch.FS, type: 'list',  data: { path } }
 * Reads:  { ch: Ch.FS, type: 'list',  data: { cwd, entries[] } }
 *         { ch: Ch.FS, type: 'error', err: string }
 *
 * Full implementation will support read, write, rename, delete.
 */

import React, { useEffect, useRef, useState } from 'react';
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

interface FileEntry {
  name:  string;
  isDir: boolean;
  size?: number;
}

interface ListResponse {
  cwd:     string;
  entries: FileEntry[];
}

export default function FilesScreen() {
  const [entries, setEntries]   = useState<FileEntry[]>([]);
  const [loading, setLoading]   = useState(false);
  const [cwd, setCwd]           = useState('~');
  const [error, setError]       = useState('');

  const cwdRef = useRef(cwd);
  cwdRef.current = cwd;

  useEffect(() => {
    if (!wsClient.isConnected) return;

    const prevOnEnvelope = wsClient.onEnvelope;
    wsClient.onEnvelope = (env: Envelope) => {
      prevOnEnvelope?.(env);
      if (env.ch !== Ch.FS) return;

      if (env.type === 'list') {
        const d = env.data as ListResponse;
        setEntries(d.entries ?? []);
        setCwd(d.cwd ?? '~');
        setLoading(false);
      }

      if (env.err) {
        setError(env.err);
        setLoading(false);
      }
    };

    listDir('~');

    return () => {
      wsClient.onEnvelope = prevOnEnvelope;
    };
  }, []);

  const listDir = (path: string) => {
    if (!wsClient.isConnected) return;
    setLoading(true);
    setError('');
    wsClient.send({ ch: Ch.FS, type: 'list', data: { path } });
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
        <Text style={styles.cwd} numberOfLines={1}>{cwd}</Text>
        <TouchableOpacity onPress={() => listDir(cwdRef.current)}>
          <Text style={styles.refresh}>Refresh</Text>
        </TouchableOpacity>
      </View>

      {error ? <Text style={styles.error}>{error}</Text> : null}

      {loading ? (
        <ActivityIndicator style={{ marginTop: 32 }} color="#4fc3f7" />
      ) : (
        <FlatList
          data={entries}
          keyExtractor={(item) => item.name}
          renderItem={({ item }) => (
            <TouchableOpacity
              style={styles.entry}
              onPress={() => item.isDir && listDir(`${cwdRef.current}/${item.name}`)}
            >
              <Text style={styles.entryIcon}>{item.isDir ? 'D' : 'F'}</Text>
              <Text style={styles.entryName} numberOfLines={1}>{item.name}</Text>
              {!item.isDir && item.size !== undefined
                ? <Text style={styles.entrySize}>{formatSize(item.size)}</Text>
                : null}
            </TouchableOpacity>
          )}
          ItemSeparatorComponent={() => <View style={styles.separator} />}
        />
      )}
    </View>
  );
}

function formatSize(bytes: number): string {
  if (bytes < 1024)           return `${bytes} B`;
  if (bytes < 1024 * 1024)    return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
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
  notice:    { color: '#aaaaaa', fontSize: 15, textAlign: 'center' },
  header:    {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: 12,
    backgroundColor: '#111111',
    borderBottomWidth: 1,
    borderBottomColor: '#222222',
  },
  cwd:       { color: '#4fc3f7', fontSize: 14, fontFamily: 'monospace', flex: 1 },
  refresh:   { color: '#aaaaaa', fontSize: 13, marginLeft: 8 },
  error:     { color: '#ef5350', padding: 12, fontSize: 13 },
  entry:     {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
  },
  entryIcon: { color: '#555555', width: 20, fontSize: 12, fontFamily: 'monospace' },
  entryName: { color: '#e0e0e0', fontSize: 15, flex: 1 },
  entrySize: { color: '#666666', fontSize: 12, marginLeft: 8 },
  separator: { height: 1, backgroundColor: '#1a1a1a', marginLeft: 36 },
});
