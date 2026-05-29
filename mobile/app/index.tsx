/**
 * Pair screen – two ways to pair with a host:
 *   1. Scan a QR code (expo-camera) that encodes { url, deviceID?, pairingCode }.
 *   2. Enter the host URL + 6-digit pairing code by hand. This is the only
 *      practical path on desktop web (no camera) and a fallback when the QR
 *      can't be scanned.
 *
 * Both paths converge on doPair(url, code): connect(url) then pair(code).
 */

import { CameraView, useCameraPermissions } from 'expo-camera';
import * as SecureStore from 'expo-secure-store';
import React, { useCallback, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Platform,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import { wsClient } from '../lib/ws';

// SecureStore keys may only contain alphanumerics, ".", "-", "_" (no ":").
const STORAGE_KEY = 'remote_host_last_pair';
const DEFAULT_URL = 'ws://127.0.0.1:8722';

interface QRPayload {
  url: string;
  pairingCode: string;
}

type Status =
  | 'idle'
  | 'manual'
  | 'scanning'
  | 'connecting'
  | 'pairing'
  | 'paired'
  | 'error';

export default function PairScreen() {
  const [permission, requestPermission] = useCameraPermissions();
  const [status, setStatus] = useState<Status>('idle');
  const [errorMsg, setErrorMsg] = useState('');
  const [manualUrl, setManualUrl] = useState(DEFAULT_URL);
  const [manualCode, setManualCode] = useState('');
  const scannedRef = useRef(false);

  // Shared pairing flow used by both QR scan and manual entry.
  const doPair = useCallback(async (url: string, code: string) => {
    try {
      setStatus('connecting');
      await wsClient.connect(url);

      setStatus('pairing');
      await wsClient.pair(code);

      // Remember the host for convenience — best-effort only: a storage failure
      // (e.g. SecureStore unavailable on web) must not fail an otherwise-
      // successful pairing.
      try {
        await SecureStore.setItemAsync(STORAGE_KEY, JSON.stringify({ url }));
      } catch {
        /* ignore — pairing already succeeded */
      }
      setStatus('paired');
    } catch (e) {
      setErrorMsg((e as Error).message ?? 'Pairing failed');
      setStatus('error');
      scannedRef.current = false;
    }
  }, []);

  const handleBarcode = useCallback(
    async ({ data }: { data: string }) => {
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
      await doPair(payload.url, payload.pairingCode);
    },
    [doPair],
  );

  const submitManual = useCallback(() => {
    const url = manualUrl.trim();
    const code = manualCode.trim();
    if (!url || !code) {
      setErrorMsg('Enter both the host URL and the pairing code.');
      setStatus('error');
      return;
    }
    void doPair(url, code);
  }, [manualUrl, manualCode, doPair]);

  const reset = () => {
    scannedRef.current = false;
    setStatus('idle');
    setErrorMsg('');
  };

  // ── Idle: choose how to pair
  if (status === 'idle') {
    return (
      <View style={styles.center}>
        <Text style={styles.heading}>Connect to a Host</Text>
        <Text style={styles.label}>
          Run <Text style={styles.mono}>hostd start</Text> on your machine, then scan the QR
          code — or enter the host URL and 6-digit code shown in the terminal.
        </Text>
        {/* QR scanning needs a camera; hidden on web where it isn't available. */}
        {Platform.OS !== 'web' && (
          <TouchableOpacity style={styles.button} onPress={() => setStatus('scanning')}>
            <Text style={styles.buttonText}>Scan QR Code</Text>
          </TouchableOpacity>
        )}
        <TouchableOpacity
          style={[styles.button, styles.secondaryButton]}
          onPress={() => {
            setErrorMsg('');
            setStatus('manual');
          }}
        >
          <Text style={[styles.buttonText, styles.secondaryButtonText]}>Enter Code Manually</Text>
        </TouchableOpacity>
      </View>
    );
  }

  // ── Manual entry
  if (status === 'manual') {
    return (
      <View style={styles.center}>
        <Text style={styles.heading}>Enter Pairing Details</Text>
        <Text style={styles.fieldLabel}>Host URL</Text>
        <TextInput
          style={styles.input}
          value={manualUrl}
          onChangeText={setManualUrl}
          autoCapitalize="none"
          autoCorrect={false}
          placeholder="ws://127.0.0.1:8722"
          placeholderTextColor="#666"
          keyboardType="url"
        />
        <Text style={styles.fieldLabel}>Pairing Code</Text>
        <TextInput
          style={[styles.input, styles.codeInput]}
          value={manualCode}
          onChangeText={setManualCode}
          autoCapitalize="none"
          autoCorrect={false}
          placeholder="123456"
          placeholderTextColor="#666"
          keyboardType="number-pad"
          maxLength={8}
          onSubmitEditing={submitManual}
          returnKeyType="go"
        />
        <TouchableOpacity style={styles.button} onPress={submitManual}>
          <Text style={styles.buttonText}>Pair</Text>
        </TouchableOpacity>
        <TouchableOpacity style={styles.linkButton} onPress={reset}>
          <Text style={styles.linkText}>Back</Text>
        </TouchableOpacity>
      </View>
    );
  }

  // ── Permission not yet resolved (native only)
  if (!permission) {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" color="#4fc3f7" />
      </View>
    );
  }

  // ── Permission denied
  if (status === 'scanning' && !permission.granted) {
    return (
      <View style={styles.center}>
        <Text style={styles.label}>Camera access is required to scan a QR code.</Text>
        <TouchableOpacity style={styles.button} onPress={requestPermission}>
          <Text style={styles.buttonText}>Grant Permission</Text>
        </TouchableOpacity>
        <TouchableOpacity style={styles.linkButton} onPress={reset}>
          <Text style={styles.linkText}>Enter code manually instead</Text>
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
        <Text style={styles.label}>{status === 'connecting' ? 'Connecting…' : 'Pairing…'}</Text>
      </View>
    );
  }

  // ── Success
  if (status === 'paired') {
    return (
      <View style={styles.center}>
        <Text style={styles.heading}>Paired!</Text>
        <Text style={styles.label}>Connected. Switch to Terminal, Files, or Monitor tabs.</Text>
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
      <TouchableOpacity style={styles.button} onPress={() => setStatus('manual')}>
        <Text style={styles.buttonText}>Try Again</Text>
      </TouchableOpacity>
      <TouchableOpacity style={styles.linkButton} onPress={reset}>
        <Text style={styles.linkText}>Start over</Text>
      </TouchableOpacity>
    </View>
  );
}

const styles = StyleSheet.create({
  full: { flex: 1, backgroundColor: '#000' },
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
  mono: { fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace', color: '#4fc3f7' },
  fieldLabel: {
    alignSelf: 'stretch',
    fontSize: 13,
    color: '#888',
    marginBottom: 6,
    marginTop: 8,
  },
  input: {
    alignSelf: 'stretch',
    backgroundColor: '#1a1a1a',
    borderColor: '#333',
    borderWidth: 1,
    borderRadius: 8,
    paddingHorizontal: 14,
    paddingVertical: 12,
    color: '#e0e0e0',
    fontSize: 16,
    marginBottom: 8,
  },
  codeInput: { letterSpacing: 4, fontSize: 22, textAlign: 'center' },
  button: {
    backgroundColor: '#4fc3f7',
    paddingHorizontal: 28,
    paddingVertical: 12,
    borderRadius: 8,
    marginTop: 12,
    alignSelf: 'stretch',
    alignItems: 'center',
  },
  secondaryButton: { backgroundColor: 'transparent', borderColor: '#4fc3f7', borderWidth: 1 },
  secondaryButtonText: { color: '#4fc3f7' },
  cancelButton: {
    position: 'absolute',
    bottom: 40,
    alignSelf: 'center',
    backgroundColor: 'rgba(0,0,0,0.6)',
    paddingHorizontal: 28,
    paddingVertical: 12,
    borderRadius: 8,
  },
  buttonText: { color: '#0d0d0d', fontWeight: '700', fontSize: 16 },
  linkButton: { marginTop: 16, padding: 8 },
  linkText: { color: '#4fc3f7', fontSize: 14 },
  overlay: { position: 'absolute', top: 60, left: 0, right: 0, alignItems: 'center' },
  overlayText: {
    color: '#ffffff',
    backgroundColor: 'rgba(0,0,0,0.5)',
    padding: 8,
    borderRadius: 6,
    fontSize: 14,
  },
});
