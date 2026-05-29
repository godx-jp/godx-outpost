/**
 * Monitor screen – stub that subscribes to the "sys" channel via wsClient.
 *
 * Sends:  { ch: Ch.Sys, type: 'poll' }  (every 2 s)
 *         { ch: Ch.Sys, type: 'stop' }  (on unmount)
 * Reads:  { ch: Ch.Sys, type: 'snapshot', data: SysSnapshot }
 *
 * Full implementation will also surface running processes and let the user
 * send signals (kill, SIGSTOP, etc.).
 */

import React, { useEffect, useRef, useState } from 'react';
import {
  ScrollView,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import { Ch, type Envelope } from '../lib/protocol';
import { wsClient } from '../lib/ws';

interface SysSnapshot {
  cpu?:      number;
  memUsed?:  number;
  memTotal?: number;
  diskUsed?: number;
  diskTotal?:number;
  uptime?:   number;
  loadAvg?:  [number, number, number];
}

export default function MonitorScreen() {
  const [snapshot, setSnapshot] = useState<SysSnapshot | null>(null);
  const [paused, setPaused]     = useState(false);
  const pausedRef               = useRef(paused);
  pausedRef.current             = paused;
  const intervalRef             = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    if (!wsClient.isConnected) return;

    const prevOnEnvelope = wsClient.onEnvelope;
    wsClient.onEnvelope = (env: Envelope) => {
      prevOnEnvelope?.(env);
      if (env.ch !== Ch.Sys || env.type !== 'snapshot') return;
      if (!pausedRef.current) setSnapshot(env.data as SysSnapshot);
    };

    const poll = () => wsClient.send({ ch: Ch.Sys, type: 'poll' });
    poll();
    intervalRef.current = setInterval(poll, 2000);

    return () => {
      wsClient.onEnvelope = prevOnEnvelope;
      if (intervalRef.current) clearInterval(intervalRef.current);
      wsClient.send({ ch: Ch.Sys, type: 'stop' });
    };
  }, []);

  if (!wsClient.isConnected) {
    return (
      <View style={styles.center}>
        <Text style={styles.notice}>Not paired. Go to the Pair tab first.</Text>
      </View>
    );
  }

  return (
    <ScrollView style={styles.container} contentContainerStyle={styles.content}>
      <View style={styles.headerRow}>
        <Text style={styles.heading}>System Monitor</Text>
        <TouchableOpacity onPress={() => setPaused((p) => !p)}>
          <Text style={styles.pauseBtn}>{paused ? 'Resume' : 'Pause'}</Text>
        </TouchableOpacity>
      </View>

      {snapshot ? (
        <>
          <MetricCard
            label="CPU"
            value={snapshot.cpu !== undefined ? `${snapshot.cpu.toFixed(1)} %` : '—'}
          />
          <MetricCard
            label="Memory"
            value={
              snapshot.memUsed !== undefined && snapshot.memTotal !== undefined
                ? `${formatMB(snapshot.memUsed)} / ${formatMB(snapshot.memTotal)}`
                : '—'
            }
          />
          <MetricCard
            label="Disk"
            value={
              snapshot.diskUsed !== undefined && snapshot.diskTotal !== undefined
                ? `${formatGB(snapshot.diskUsed)} / ${formatGB(snapshot.diskTotal)}`
                : '—'
            }
          />
          <MetricCard
            label="Load avg"
            value={
              snapshot.loadAvg
                ? snapshot.loadAvg.map((v) => v.toFixed(2)).join('  ')
                : '—'
            }
          />
          <MetricCard
            label="Uptime"
            value={snapshot.uptime !== undefined ? formatUptime(snapshot.uptime) : '—'}
          />
        </>
      ) : (
        <Text style={styles.waiting}>Waiting for data…</Text>
      )}
    </ScrollView>
  );
}

function MetricCard({ label, value }: { label: string; value: string }) {
  return (
    <View style={styles.card}>
      <Text style={styles.cardLabel}>{label}</Text>
      <Text style={styles.cardValue}>{value}</Text>
    </View>
  );
}

function formatMB(b: number)  { return `${(b / 1024 / 1024).toFixed(0)} MB`; }
function formatGB(b: number)  { return `${(b / 1024 / 1024 / 1024).toFixed(1)} GB`; }
function formatUptime(s: number) {
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return `${h}h ${m}m`;
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#0d0d0d' },
  content:   { padding: 16 },
  center:    {
    flex: 1,
    backgroundColor: '#0d0d0d',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 24,
  },
  notice:    { color: '#aaaaaa', fontSize: 15, textAlign: 'center' },
  headerRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginBottom: 16,
  },
  heading:   { color: '#e0e0e0', fontSize: 20, fontWeight: '700' },
  pauseBtn:  { color: '#4fc3f7', fontSize: 14 },
  waiting:   { color: '#555555', fontSize: 14, textAlign: 'center', marginTop: 40 },
  card:      {
    backgroundColor: '#111111',
    borderRadius: 10,
    padding: 16,
    marginBottom: 12,
    borderWidth: 1,
    borderColor: '#222222',
  },
  cardLabel: { color: '#777777', fontSize: 12, marginBottom: 6, letterSpacing: 0.8 },
  cardValue: {
    color: '#e0e0e0',
    fontSize: 22,
    fontWeight: '600',
    fontFamily: 'monospace',
  },
});
