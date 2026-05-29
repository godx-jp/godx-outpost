/**
 * Hosts screen – manage and connect to multiple paired hosts (Termius-style).
 *
 * - Lists saved hosts; tap one to connect (switches the active connection that
 *   the Terminal/Files/Monitor tabs use).
 * - "Add Host" pairs a new host by QR scan (native) or manual URL + 6-digit code.
 * - Each host keeps its own tokens (lib/hosts.ts); one host is active at a time.
 * - On launch the last active host is auto-reconnected with its stored token.
 */

import { CameraView, useCameraPermissions } from 'expo-camera';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  ActivityIndicator, FlatList, Platform, StyleSheet, Text, TextInput, TouchableOpacity, View,
} from 'react-native';
import {
  defaultHostName, getActiveHostId, listHosts, removeHost, saveHost,
  setActiveHostId, updateHostTokens, type Host,
} from '../lib/hosts';
import { wsClient } from '../lib/ws';

const DEFAULT_URL = 'ws://127.0.0.1:8722';

type Phase = 'loading' | 'hosts' | 'add' | 'scan';

export default function HostsScreen() {
  const [permission, requestPermission] = useCameraPermissions();
  const [phase, setPhase]       = useState<Phase>('loading');
  const [hosts, setHosts]       = useState<Host[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [busy, setBusy]         = useState('');
  const [error, setError]       = useState('');
  const [manualUrl, setManualUrl]   = useState(DEFAULT_URL);
  const [manualCode, setManualCode] = useState('');

  const activeIdRef = useRef<string | null>(null);
  activeIdRef.current = activeId;
  const scannedRef = useRef(false);

  const reloadHosts = useCallback(async () => {
    setHosts(await listHosts());
  }, []);

  // Mount: persist refreshed tokens to the right host, then auto-reconnect.
  useEffect(() => {
    wsClient.onTokens = (access, refresh) => {
      const id = activeIdRef.current;
      if (id) void updateHostTokens(id, access, refresh);
    };

    let cancelled = false;
    (async () => {
      const hs = await listHosts();
      if (cancelled) return;
      setHosts(hs);
      const aid = await getActiveHostId();
      const h = aid ? hs.find((x) => x.id === aid) : undefined;
      if (h) {
        setBusy(`Reconnecting to ${h.name}…`);
        wsClient.setTokens(h.access, h.refresh);
        wsClient.activeHostId = h.id;
        wsClient.activeHostName = h.name;
        activeIdRef.current = h.id;
        const ok = await wsClient.resume(h.url);
        if (!cancelled && ok) setActiveId(h.id);
      }
      if (!cancelled) { setBusy(''); setPhase('hosts'); }
    })();

    return () => {
      cancelled = true;
      wsClient.onTokens = null;
    };
  }, []);

  // Connect (or switch) to a saved host.
  const connectHost = useCallback(async (h: Host) => {
    if (h.id === activeIdRef.current && wsClient.isAuthed) return;
    setError('');
    setBusy(`Connecting to ${h.name}…`);
    wsClient.disconnect();
    wsClient.clearTokens();
    wsClient.setTokens(h.access, h.refresh);
    wsClient.activeHostId = h.id;
    wsClient.activeHostName = h.name;
    activeIdRef.current = h.id;
    await setActiveHostId(h.id);
    const ok = await wsClient.resume(h.url);
    if (ok) {
      setActiveId(h.id);
    } else {
      setActiveId(null);
      setError(`Could not authenticate with ${h.name}. It may have been revoked — remove and re-pair.`);
    }
    setBusy('');
  }, []);

  // Pair a brand-new host and save it.
  const doAddPair = useCallback(async (url: string, code: string) => {
    setError('');
    try {
      setBusy('Connecting…');
      wsClient.disconnect();
      wsClient.clearTokens();
      await wsClient.connect(url);
      setBusy('Pairing…');
      const r = await wsClient.pair(code);
      const host: Host = {
        id: r.deviceId, name: defaultHostName(url), url,
        access: r.access, refresh: r.refresh,
      };
      await saveHost(host);
      await setActiveHostId(host.id);
      wsClient.activeHostId = host.id;
      wsClient.activeHostName = host.name;
      activeIdRef.current = host.id;
      setActiveId(host.id);
      await reloadHosts();
      setManualCode('');
      setBusy('');
      setPhase('hosts');
    } catch (e) {
      setBusy('');
      setError((e as Error).message ?? 'Pairing failed');
    }
  }, [reloadHosts]);

  const onRemove = useCallback(async (h: Host) => {
    if (h.id === activeIdRef.current) {
      wsClient.disconnect();
      wsClient.activeHostId = null;
      wsClient.activeHostName = null;
      setActiveId(null);
    }
    await removeHost(h.id);
    await reloadHosts();
  }, [reloadHosts]);

  const handleBarcode = useCallback(({ data }: { data: string }) => {
    if (scannedRef.current) return;
    scannedRef.current = true;
    try {
      const p = JSON.parse(data) as { url: string; pairingCode: string };
      if (!p.url || !p.pairingCode) throw new Error('Invalid QR payload.');
      setPhase('add');
      void doAddPair(p.url, p.pairingCode);
    } catch (e) {
      setError((e as Error).message);
      setPhase('add');
    } finally {
      scannedRef.current = false;
    }
  }, [doAddPair]);

  // ── Loading / busy ──────────────────────────────────────────────────────────
  if (phase === 'loading' || busy) {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" color="#4fc3f7" />
        {busy ? <Text style={[styles.sub, { marginTop: 16 }]}>{busy}</Text> : null}
      </View>
    );
  }

  // ── QR scanning ───────────────────────────────────────────────────────────
  if (phase === 'scan') {
    if (!permission?.granted) {
      return (
        <View style={styles.center}>
          <Text style={styles.sub}>Camera access is required to scan a QR code.</Text>
          <TouchableOpacity style={styles.btn} onPress={requestPermission}>
            <Text style={styles.btnText}>Grant Permission</Text>
          </TouchableOpacity>
          <TouchableOpacity style={styles.link} onPress={() => setPhase('add')}>
            <Text style={styles.linkText}>Enter manually instead</Text>
          </TouchableOpacity>
        </View>
      );
    }
    return (
      <View style={styles.full}>
        <CameraView
          style={StyleSheet.absoluteFill}
          facing="back"
          onBarcodeScanned={handleBarcode}
          barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
        />
        <TouchableOpacity style={styles.cancel} onPress={() => setPhase('add')}>
          <Text style={styles.btnText}>Cancel</Text>
        </TouchableOpacity>
      </View>
    );
  }

  // ── Add a new host ──────────────────────────────────────────────────────────
  if (phase === 'add') {
    return (
      <View style={styles.containerPad}>
        <Text style={styles.heading}>Add a Host</Text>
        <Text style={styles.sub}>
          Run <Text style={styles.mono}>hostd start</Text> and enter its URL + 6-digit code,
          {Platform.OS !== 'web' ? ' or scan the QR.' : ''}
        </Text>
        {error ? <Text style={styles.error}>{error}</Text> : null}

        <Text style={styles.fieldLabel}>Host URL</Text>
        <TextInput
          style={styles.input} value={manualUrl} onChangeText={setManualUrl}
          autoCapitalize="none" autoCorrect={false} placeholder={DEFAULT_URL} placeholderTextColor="#666"
          keyboardType="url"
        />
        <Text style={styles.fieldLabel}>Pairing Code</Text>
        <TextInput
          style={[styles.input, styles.code]} value={manualCode} onChangeText={setManualCode}
          autoCapitalize="none" autoCorrect={false} placeholder="123456" placeholderTextColor="#666"
          keyboardType="number-pad" maxLength={8}
          onSubmitEditing={() => doAddPair(manualUrl.trim(), manualCode.trim())} returnKeyType="go"
        />
        <TouchableOpacity style={styles.btn} onPress={() => doAddPair(manualUrl.trim(), manualCode.trim())}>
          <Text style={styles.btnText}>Pair</Text>
        </TouchableOpacity>
        {Platform.OS !== 'web' ? (
          <TouchableOpacity
            style={[styles.btn, styles.outline]}
            onPress={() => { setError(''); setPhase('scan'); }}
          >
            <Text style={[styles.btnText, styles.outlineText]}>Scan QR Code</Text>
          </TouchableOpacity>
        ) : null}
        <TouchableOpacity style={styles.link} onPress={() => { setError(''); setPhase('hosts'); }}>
          <Text style={styles.linkText}>Back to hosts</Text>
        </TouchableOpacity>
      </View>
    );
  }

  // ── Hosts list ───────────────────────────────────────────────────────────────
  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.heading}>Hosts</Text>
        <TouchableOpacity onPress={() => { setError(''); setPhase('add'); }}>
          <Text style={styles.headerBtn}>+ Add</Text>
        </TouchableOpacity>
      </View>
      {error ? <Text style={styles.error}>{error}</Text> : null}

      <FlatList
        data={hosts}
        keyExtractor={(h) => h.id}
        ListEmptyComponent={
          <Text style={styles.empty}>No hosts yet. Tap “+ Add” to pair your first host.</Text>
        }
        renderItem={({ item }) => {
          const connected = item.id === activeId && wsClient.isAuthed;
          return (
            <TouchableOpacity style={styles.row} onPress={() => connectHost(item)}>
              <View style={[styles.dot, connected ? styles.dotOn : styles.dotOff]} />
              <View style={{ flex: 1 }}>
                <Text style={styles.rowName}>{item.name}</Text>
                <Text style={styles.rowSub} numberOfLines={1}>
                  {item.url}{connected ? ' · connected' : ''}
                </Text>
              </View>
              <TouchableOpacity onPress={() => onRemove(item)} hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}>
                <Text style={styles.remove}>Remove</Text>
              </TouchableOpacity>
            </TouchableOpacity>
          );
        }}
        ItemSeparatorComponent={() => <View style={styles.sep} />}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  full:         { flex: 1, backgroundColor: '#000' },
  container:    { flex: 1, backgroundColor: '#0d0d0d' },
  containerPad: { flex: 1, backgroundColor: '#0d0d0d', padding: 24, justifyContent: 'center' },
  center:       { flex: 1, backgroundColor: '#0d0d0d', alignItems: 'center', justifyContent: 'center', padding: 24 },
  heading:      { color: '#e0e0e0', fontSize: 24, fontWeight: '700' },
  sub:          { color: '#aaa', fontSize: 15, textAlign: 'center', lineHeight: 22, marginTop: 8 },
  mono:         { fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace', color: '#4fc3f7' },
  error:        { color: '#ef5350', fontSize: 13, marginVertical: 10 },
  header:       { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', padding: 16, borderBottomWidth: 1, borderBottomColor: '#222' },
  headerBtn:    { color: '#4fc3f7', fontSize: 16, fontWeight: '600' },
  empty:        { color: '#555', textAlign: 'center', marginTop: 40, fontSize: 14, paddingHorizontal: 24 },
  row:          { flexDirection: 'row', alignItems: 'center', paddingHorizontal: 16, paddingVertical: 16 },
  dot:          { width: 10, height: 10, borderRadius: 5, marginRight: 12 },
  dotOn:        { backgroundColor: '#4caf50' },
  dotOff:       { backgroundColor: '#444' },
  rowName:      { color: '#e0e0e0', fontSize: 16 },
  rowSub:       { color: '#666', fontSize: 12, fontFamily: 'monospace', marginTop: 2 },
  remove:       { color: '#ef5350', fontSize: 13 },
  sep:          { height: 1, backgroundColor: '#1a1a1a', marginLeft: 38 },
  fieldLabel:   { alignSelf: 'stretch', color: '#888', fontSize: 13, marginTop: 14, marginBottom: 6 },
  input:        { alignSelf: 'stretch', backgroundColor: '#1a1a1a', borderColor: '#333', borderWidth: 1, borderRadius: 8, paddingHorizontal: 14, paddingVertical: 12, color: '#e0e0e0', fontSize: 16 },
  code:         { letterSpacing: 4, fontSize: 22, textAlign: 'center' },
  btn:          { backgroundColor: '#4fc3f7', paddingVertical: 13, borderRadius: 8, marginTop: 16, alignSelf: 'stretch', alignItems: 'center' },
  outline:      { backgroundColor: 'transparent', borderColor: '#4fc3f7', borderWidth: 1 },
  outlineText:  { color: '#4fc3f7' },
  btnText:      { color: '#0d0d0d', fontWeight: '700', fontSize: 16 },
  link:         { marginTop: 16, padding: 8, alignSelf: 'center' },
  linkText:     { color: '#4fc3f7', fontSize: 14 },
  cancel:       { position: 'absolute', bottom: 40, alignSelf: 'center', backgroundColor: 'rgba(0,0,0,0.6)', paddingHorizontal: 28, paddingVertical: 12, borderRadius: 8 },
});
