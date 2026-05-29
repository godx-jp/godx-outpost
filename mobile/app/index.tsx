/**
 * Pair screen – uses expo-camera to scan a QR code that encodes:
 *   { url: string; pairingCode: string }
 * On scan, calls wsClient.connect(url) then wsClient.pair(pairingCode).
 */

import { CameraView, useCameraPermissions } from 'expo-camera';
import * as SecureStore from 'expo-secure-store';
import React, { useCallback, useRef, useState } from 'react';
import {
  ActivityIndicator,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import { wsClient } from '../lib/ws';

const STORAGE_KEY = 'remote-host:last-pair';

interface QRPayload {
  url: string;
  pairingCode: string;
}

type Status = 'idle' | 'scanning' | 'connecting' | 'pairing' | 'paired' | 'error';

export default function PairScreen() {
  const [permission, requestPermission] = useCameraPermissions();
  const [status, setStatus] = useState<Status>('idle');
  const [errorMsg, setErrorMsg] = useState('');
  const scannedRef = useRef(false);

  const handleBarcode = useCallback(async ({ data }: { data: string }) => {
    if (scannedRef.current) return;
    scannedRef.current = true;

    let payload: QRPayload;
    try {
      payload = JSON.parse(data) as QRPayload;
      if (!payload.url || !payload.pairingCode) {
        throw new Error('Invalid QR payload – missing url or pairingCode.');
      }
    } catch (e) {
      setErrorMsg((e as Error).message);
      setStatus('error');
      scannedRef.current = false;
      return;
    }

    try {
      setStatus('connecting');
      await wsClient.connect(payload.url);

      setStatus('pairing');
      await wsClient.pair(payload.pairingCode);

      await SecureStore.setItemAsync(STORAGE_KEY, JSON.stringify(payload));
      setStatus('paired');
    } catch (e) {
      setErrorMsg((e as Error).message ?? 'Pairing failed');
      setStatus('error');
      scannedRef.current = false;
    }
  }, []);

  const reset = () => {
    scannedRef.current = false;
    setStatus('scanning');
    setErrorMsg('');
  };

  // ── Permission not yet resolved
  if (!permission) {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" color="#4fc3f7" />
      </View>
    );
  }

  // ── Permission denied
  if (!permission.granted) {
    return (
      <View style={styles.center}>
        <Text style={styles.label}>Camera access is required to pair.</Text>
        <TouchableOpacity style={styles.button} onPress={requestPermission}>
          <Text style={styles.buttonText}>Grant Permission</Text>
        </TouchableOpacity>
      </View>
    );
  }

  // ── Idle: not yet started
  if (status === 'idle') {
    return (
      <View style={styles.center}>
        <Text style={styles.heading}>Connect to a Host</Text>
        <Text style={styles.label}>
          Display the pairing QR code on your host machine, then tap Scan.
        </Text>
        <TouchableOpacity style={styles.button} onPress={() => setStatus('scanning')}>
          <Text style={styles.buttonText}>Scan QR Code</Text>
        </TouchableOpacity>
      </View>
    );
  }

  // ── Scanning
  if (status === 'scanning') {
    return (
      <View style={styles.full}>
        <CameraView
          style={StyleSheet.absoluteFill}
          facing="back"
          onBarcodeScanned={handleBarcode}
          barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
        />
        <View style={styles.overlay}>
          <Text style={styles.overlayText}>Align the QR code within the frame</Text>
        </View>
        <TouchableOpacity style={styles.cancelButton} onPress={() => setStatus('idle')}>
          <Text style={styles.buttonText}>Cancel</Text>
        </TouchableOpacity>
      </View>
    );
  }

  // ── In-progress states
  if (status === 'connecting' || status === 'pairing') {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" color="#4fc3f7" />
        <Text style={styles.label}>
          {status === 'connecting' ? 'Connecting…' : 'Pairing…'}
        </Text>
      </View>
    );
  }

  // ── Success
  if (status === 'paired') {
    return (
      <View style={styles.center}>
        <Text style={styles.heading}>Paired!</Text>
        <Text style={styles.label}>
          Connected. Switch to Terminal, Files, or Monitor tabs.
        </Text>
        <TouchableOpacity style={styles.button} onPress={reset}>
          <Text style={styles.buttonText}>Pair a Different Host</Text>
        </TouchableOpacity>
      </View>
    );
  }

  // ── Error
  return (
    <View style={styles.center}>
      <Text style={[styles.heading, { color: '#ef5350' }]}>Pairing Failed</Text>
      <Text style={styles.label}>{errorMsg}</Text>
      <TouchableOpacity style={styles.button} onPress={reset}>
        <Text style={styles.buttonText}>Try Again</Text>
      </TouchableOpacity>
    </View>
  );
}

const styles = StyleSheet.create({
  full:   { flex: 1, backgroundColor: '#000' },
  center: {
    flex: 1,
    backgroundColor: '#0d0d0d',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 24,
  },
  heading: {
    fontSize: 24,
    fontWeight: '700',
    color: '#e0e0e0',
    marginBottom: 12,
    textAlign: 'center',
  },
  label: {
    fontSize: 15,
    color: '#aaaaaa',
    textAlign: 'center',
    marginBottom: 24,
    lineHeight: 22,
  },
  button: {
    backgroundColor: '#4fc3f7',
    paddingHorizontal: 28,
    paddingVertical: 12,
    borderRadius: 8,
  },
  cancelButton: {
    position: 'absolute',
    bottom: 40,
    alignSelf: 'center',
    backgroundColor: 'rgba(0,0,0,0.6)',
    paddingHorizontal: 28,
    paddingVertical: 12,
    borderRadius: 8,
  },
  buttonText:  { color: '#0d0d0d', fontWeight: '700', fontSize: 16 },
  overlay:     { position: 'absolute', top: 60, left: 0, right: 0, alignItems: 'center' },
  overlayText: {
    color: '#ffffff',
    backgroundColor: 'rgba(0,0,0,0.5)',
    padding: 8,
    borderRadius: 6,
    fontSize: 14,
  },
});
