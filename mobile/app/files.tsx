/**
 * Files screen – stub that uses the "fs" channel via wsClient.
 *
 * Sends:  { ch: Ch.FS, type: 'list',  data: { path } }
 * Reads:  { ch: Ch.FS, type: 'list',  data: { cwd, entries[] } }
 *         { ch: Ch.FS, type: 'error', err: string }
 *
 * Full implementation will support read, write, rename, delete. UI uses
 * react-native-paper; colour comes from the theme, StyleSheet holds layout only.
 */

import { router, useFocusEffect } from 'expo-router';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { FlatList, StyleSheet, View } from 'react-native';
import {
  ActivityIndicator, Appbar, Dialog, Divider, HelperText, List, Portal, Text,
} from 'react-native-paper';
import { Ch, type Envelope } from '../lib/protocol';
import { DEFAULT_FILES_DIR, getDefaultDir } from '../lib/settings';
import { buildLaunchCmd, type LaunchKind, setTermLaunch } from '../lib/termLaunch';
import { useAuthed } from '../lib/useConn';
import { wsClient } from '../lib/ws';

// Long-press launch options, in display order.
const LAUNCH_OPTIONS: { kind: LaunchKind; label: string; icon: string }[] = [
  { kind: 'term',        label: 'Mở Terminal tại đây',        icon: 'console' },
  { kind: 'claude',      label: 'Mở Claude',                  icon: 'robot-outline' },
  { kind: 'claude-yolo', label: 'Mở Claude (cấp mọi quyền)',  icon: 'robot-excited-outline' },
  { kind: 'codex',       label: 'Mở Codex',                   icon: 'code-tags' },
  { kind: 'codex-yolo',  label: 'Mở Codex (cấp mọi quyền)',   icon: 'code-greater-than' },
];

interface FileEntry {
  name:  string;
  isDir: boolean;
  size?: number;
}

// The server's fs "list" reply carries only { entries }. The current directory
// is tracked client-side from the path we requested.
interface ListResponse {
  entries: FileEntry[];
}

export default function FilesScreen() {
  const authed = useAuthed();
  const [entries, setEntries]   = useState<FileEntry[]>([]);
  const [loading, setLoading]   = useState(false);
  const [cwd, setCwd]           = useState(DEFAULT_FILES_DIR);
  const [error, setError]       = useState('');
  // Stack of parent directories visited, so "back" returns to the folder we
  // came from. Empty at the home directory (no back button shown).
  const [stack, setStack]       = useState<string[]>([]);
  // Folder name the long-press launch menu is open for (null = closed).
  const [menuFor, setMenuFor]   = useState<string | null>(null);

  const cwdRef = useRef(cwd);
  cwdRef.current = cwd;
  // Path of the in-flight list request; promoted to cwd when it succeeds.
  const pendingPathRef = useRef(DEFAULT_FILES_DIR);
  const lastHostRef = useRef<string | null>(wsClient.activeHostId);
  // Configurable default directory (Settings); the "projects" shortcut target.
  const homeDirRef = useRef(DEFAULT_FILES_DIR);

  useEffect(() => {
    if (!authed) return;

    const prevOnEnvelope = wsClient.onEnvelope;
    wsClient.onEnvelope = (env: Envelope) => {
      prevOnEnvelope?.(env);
      if (env.ch !== Ch.FS) return;

      // Error envelopes carry no data — handle them first to avoid touching
      // env.data (which is undefined on error).
      if (env.err) {
        setError(env.err);
        setLoading(false);
        return;
      }

      if (env.type === 'list') {
        const d = (env.data ?? {}) as ListResponse;
        setEntries(d.entries ?? []);
        setCwd(pendingPathRef.current); // server doesn't echo cwd
        setLoading(false);
      }
    };

    // Load the configured default directory, then list it.
    void getDefaultDir().then((dir) => {
      homeDirRef.current = dir;
      listDir(dir);
    });

    return () => {
      wsClient.onEnvelope = prevOnEnvelope;
    };
  }, [authed]);

  const listDir = (path: string) => {
    if (!wsClient.isConnected) return;
    pendingPathRef.current = path;
    setLoading(true);
    setError('');
    wsClient.send({ ch: Ch.FS, type: 'list', data: { path } });
  };

  // Descend into a child directory, remembering the current one for "back".
  const enterDir = (name: string) => {
    setStack((s) => [...s, cwdRef.current]);
    listDir(`${cwdRef.current}/${name}`);
  };

  // Return to the folder we came from (parent of the current directory).
  const goBack = () => {
    if (stack.length === 0) return;
    const target = stack[stack.length - 1];
    setStack(stack.slice(0, -1));
    listDir(target);
  };

  // Jump straight back to the default directory (header shortcut).
  const goProjects = () => {
    setStack([]);
    listDir(homeDirRef.current);
  };

  // Long-press launch: queue a "run here" command and switch to the Terminal
  // tab, which opens a new session in this folder and runs it.
  const launch = (name: string, kind: LaunchKind) => {
    const path = `${cwdRef.current}/${name}`;
    setTermLaunch({ cmd: buildLaunchCmd(path, kind) });
    setMenuFor(null);
    router.navigate('/terminal');
  };

  // Files are per-host: when the active host changed (switched on the Hosts
  // tab), reset to the new host's home directory on focus.
  useFocusEffect(
    useCallback(() => {
      if (wsClient.activeHostId !== lastHostRef.current) {
        lastHostRef.current = wsClient.activeHostId;
        setEntries([]);
        setError('');
        setStack([]);
        void getDefaultDir().then((dir) => {
          homeDirRef.current = dir;
          listDir(dir);
        });
      }
    }, []),
  );

  if (!authed) {
    return (
      <View style={styles.center}>
        <Text variant="bodyLarge">No host connected. Go to the Hosts tab and connect.</Text>
      </View>
    );
  }

  return (
    <View style={styles.flex}>
      <Appbar.Header mode="small">
        {stack.length > 0 ? <Appbar.BackAction onPress={goBack} /> : null}
        <Appbar.Content title="Files" subtitle={cwd} subtitleStyle={styles.mono} />
        <Appbar.Action icon="folder-home" onPress={goProjects} />
        <Appbar.Action icon="refresh" onPress={() => listDir(cwdRef.current)} />
      </Appbar.Header>

      {error ? <HelperText type="error" visible>{error}</HelperText> : null}

      {loading ? (
        <View style={styles.center}>
          <ActivityIndicator />
        </View>
      ) : (
        <FlatList
          data={entries}
          keyExtractor={(item) => item.name}
          ItemSeparatorComponent={Divider}
          renderItem={({ item }) => (
            <List.Item
              title={item.name}
              titleNumberOfLines={1}
              onPress={() => item.isDir && enterDir(item.name)}
              onLongPress={() => item.isDir && setMenuFor(item.name)}
              left={(props) => <List.Icon {...props} icon={item.isDir ? 'folder' : 'file-outline'} />}
              right={() =>
                !item.isDir && item.size !== undefined ? (
                  <Text variant="bodySmall" style={styles.size}>{formatSize(item.size)}</Text>
                ) : null
              }
            />
          )}
        />
      )}

      <Portal>
        <Dialog visible={!!menuFor} onDismiss={() => setMenuFor(null)}>
          <Dialog.Title numberOfLines={1}>{menuFor ?? ''}</Dialog.Title>
          <Dialog.Content>
            {LAUNCH_OPTIONS.map((opt) => (
              <List.Item
                key={opt.kind}
                title={opt.label}
                onPress={() => menuFor && launch(menuFor, opt.kind)}
                left={(props) => <List.Icon {...props} icon={opt.icon} />}
              />
            ))}
          </Dialog.Content>
        </Dialog>
      </Portal>
    </View>
  );
}

function formatSize(bytes: number): string {
  if (bytes < 1024)           return `${bytes} B`;
  if (bytes < 1024 * 1024)    return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

const styles = StyleSheet.create({
  flex:   { flex: 1 },
  center: { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24 },
  mono:   { fontFamily: 'monospace', fontSize: 13 },
  size:   { alignSelf: 'center' },
});
