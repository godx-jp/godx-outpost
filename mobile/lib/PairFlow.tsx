/**
 * PairFlow — pair (or re-attach to) a host via manual URL+code or a QR scan.
 * Used by the login screen (no host yet) and by the Hosts manager's "add".
 *
 * On success it makes the host active+connected and calls onDone(); the parent
 * (or the auth gate in _layout) then shows the appropriate screen.
 */
import { CameraView, useCameraPermissions } from 'expo-camera';
import React, { useCallback, useRef, useState } from 'react';
import { Platform, StyleSheet, View } from 'react-native';
import {
  ActivityIndicator, Appbar, Button, HelperText, Text, TextInput,
} from 'react-native-paper';
import { defaultHostName, getHost, saveHost, setActiveHostId, type Host } from './hosts';
import { wsClient } from './ws';

const DEFAULT_URL = 'ws://127.0.0.1:8722';

export function PairFlow({ onDone, onBack }: { onDone: () => void; onBack?: () => void }) {
  const [permission, requestPermission] = useCameraPermissions();
  const [mode, setMode]   = useState<'form' | 'scan'>('form');
  const [url, setUrl]     = useState(DEFAULT_URL);
  const [code, setCode]   = useState('');
  const [busy, setBusy]   = useState('');
  const [error, setError] = useState('');
  const scannedRef = useRef(false);

  const pair = useCallback(async (u: string, c: string) => {
    setError('');
    try {
      setBusy('Connecting…');
      wsClient.disconnect();
      wsClient.clearTokens();
      await wsClient.connect(u);
      setBusy('Pairing…');
      const r = await wsClient.pair(c);
      const host: Host = {
        id: r.deviceId, name: defaultHostName(u), url: u, access: r.access, refresh: r.refresh,
      };
      await saveHost(host);
      await setActiveHostId(host.id);
      wsClient.activeHostId = host.id;
      wsClient.activeHostName = host.name;
      setBusy('');
      setCode('');
      onDone();
    } catch (e) {
      setBusy('');
      setError((e as Error).message ?? 'Pairing failed');
    }
  }, [onDone]);

  const handleBarcode = useCallback(({ data }: { data: string }) => {
    if (scannedRef.current) return;
    scannedRef.current = true;
    let p: { url: string; pairingCode: string; deviceID?: string };
    try {
      p = JSON.parse(data);
      if (!p.url || !p.pairingCode) throw new Error('Invalid QR payload.');
    } catch (e) {
      setError((e as Error).message);
      setMode('form');
      scannedRef.current = false;
      return;
    }
    void (async () => {
      try {
        // Already paired (by device id)? The code is single-use — reconnect
        // with the saved token instead of re-pairing.
        const existing = p.deviceID ? await getHost(p.deviceID) : undefined;
        if (existing) {
          setMode('form');
          setBusy(`Connecting to ${existing.name}…`);
          wsClient.disconnect();
          wsClient.clearTokens();
          wsClient.setTokens(existing.access, existing.refresh);
          wsClient.activeHostId = existing.id;
          wsClient.activeHostName = existing.name;
          await setActiveHostId(existing.id);
          const ok = await wsClient.resume(existing.url);
          setBusy('');
          if (ok) onDone();
          else setError('Could not authenticate — remove and re-pair.');
        } else {
          setMode('form');
          await pair(p.url, p.pairingCode);
        }
      } finally {
        scannedRef.current = false;
      }
    })();
  }, [pair, onDone]);

  if (busy) {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" />
        <Text variant="bodyMedium" style={styles.busy}>{busy}</Text>
      </View>
    );
  }

  if (mode === 'scan') {
    if (!permission?.granted) {
      return (
        <View style={styles.center}>
          <Text variant="bodyLarge" style={styles.centerText}>
            Camera access is required to scan a QR code.
          </Text>
          <Button mode="contained" onPress={requestPermission} style={styles.gap}>
            Grant Permission
          </Button>
          <Button mode="text" onPress={() => setMode('form')}>Enter manually instead</Button>
        </View>
      );
    }
    return (
      <View style={styles.flex}>
        <CameraView
          style={StyleSheet.absoluteFill}
          facing="back"
          onBarcodeScanned={handleBarcode}
          barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
        />
        <View style={styles.cancel}>
          <Button mode="contained" onPress={() => setMode('form')}>Cancel</Button>
        </View>
      </View>
    );
  }

  return (
    <View style={styles.flex}>
      <Appbar.Header mode="small">
        {onBack ? <Appbar.BackAction onPress={onBack} /> : null}
        <Appbar.Content title="Add a Host" />
      </Appbar.Header>
      <View style={styles.form}>
        <Text variant="bodyMedium">
          Run <Text style={styles.mono}>hostd start</Text> and enter its URL + 6-digit code
          {Platform.OS !== 'web' ? ', or scan the QR.' : '.'}
        </Text>
        {error ? <HelperText type="error" visible>{error}</HelperText> : null}
        <TextInput
          mode="outlined" label="Host URL" value={url} onChangeText={setUrl}
          autoCapitalize="none" autoCorrect={false} placeholder={DEFAULT_URL}
          keyboardType="url" style={styles.field}
        />
        <TextInput
          mode="outlined" label="Pairing Code" value={code} onChangeText={setCode}
          autoCapitalize="none" autoCorrect={false} placeholder="123456"
          keyboardType="number-pad" maxLength={8} returnKeyType="go"
          onSubmitEditing={() => pair(url.trim(), code.trim())} style={styles.field}
        />
        <Button mode="contained" onPress={() => pair(url.trim(), code.trim())} style={styles.gap}>
          Pair
        </Button>
        {Platform.OS !== 'web' ? (
          <Button mode="outlined" icon="qrcode-scan" onPress={() => { setError(''); setMode('scan'); }} style={styles.gap}>
            Scan QR Code
          </Button>
        ) : null}
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  flex:       { flex: 1 },
  center:     { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24 },
  centerText: { textAlign: 'center' },
  busy:       { marginTop: 16 },
  form:       { padding: 24, gap: 4 },
  field:      { marginTop: 10 },
  gap:        { marginTop: 16 },
  mono:       { fontFamily: 'monospace' },
  cancel:     { position: 'absolute', bottom: 40, alignSelf: 'center' },
});
