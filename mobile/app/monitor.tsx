/**
 * Monitor screen – subscribes to the "sys" channel via wsClient.
 *
 * Sends:  { ch: Ch.Sys, type: 'subscribe',   data: { intervalMs } }  (on mount)
 *         { ch: Ch.Sys, type: 'unsubscribe' }                        (on unmount)
 * Reads:  { ch: Ch.Sys, type: 'metrics', data: Metrics }  (pushed by the server)
 *
 * The metric shape mirrors internal/sys/sys.go exactly.
 */

import React, { useEffect, useRef, useState } from 'react';
import { ScrollView, StyleSheet, Text, TouchableOpacity, View } from 'react-native';
import { Ch, type Envelope } from '../lib/protocol';
import { wsClient } from '../lib/ws';

interface DiskStat { path: string; total: number; used: number; pct: number }
interface ProcStat { pid: number; name: string; cpu: number; mem: number }

interface Metrics {
  cpuPct: number;
  mem: { total: number; used: number; pct: number };
  swap?: { total: number; used: number; pct: number };
  disk: DiskStat[];
  net: { bytesSent: number; bytesRecv: number };
  load?: { load1: number; load5: number; load15: number };
  topProcs: ProcStat[];
  ts: number;
}

const SUBSCRIBE_INTERVAL_MS = 1500;

export default function MonitorScreen() {
  const [m, setM]           = useState<Metrics | null>(null);
  const [paused, setPaused] = useState(false);
  const pausedRef           = useRef(paused);
  pausedRef.current         = paused;

  useEffect(() => {
    if (!wsClient.isConnected) return;

    const prevOnEnvelope = wsClient.onEnvelope;
    wsClient.onEnvelope = (env: Envelope) => {
      prevOnEnvelope?.(env);
      if (env.ch !== Ch.Sys || env.type !== 'metrics') return;
      if (!pausedRef.current && env.data) setM(env.data as Metrics);
    };

    // Server pushes metrics on its own ticker — subscribe once, no client poll.
    wsClient.send({ ch: Ch.Sys, type: 'subscribe', data: { intervalMs: SUBSCRIBE_INTERVAL_MS } });

    return () => {
      wsClient.onEnvelope = prevOnEnvelope;
      try {
        wsClient.send({ ch: Ch.Sys, type: 'unsubscribe' });
      } catch {
        /* socket may be gone */
      }
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

      {!m ? (
        <Text style={styles.waiting}>Waiting for data…</Text>
      ) : (
        <>
          <MetricCard label="CPU" value={`${m.cpuPct.toFixed(1)} %`} />
          <MetricCard
            label="Memory"
            value={`${formatGB(m.mem.used)} / ${formatGB(m.mem.total)}  (${m.mem.pct.toFixed(0)}%)`}
          />
          {m.swap && m.swap.total > 0 ? (
            <MetricCard
              label="Swap"
              value={`${formatGB(m.swap.used)} / ${formatGB(m.swap.total)}  (${m.swap.pct.toFixed(0)}%)`}
            />
          ) : null}
          {m.load ? (
            <MetricCard
              label="Load avg"
              value={`${m.load.load1.toFixed(2)}  ${m.load.load5.toFixed(2)}  ${m.load.load15.toFixed(2)}`}
            />
          ) : null}
          <MetricCard
            label="Network"
            value={`↑ ${formatGB(m.net.bytesSent)}   ↓ ${formatGB(m.net.bytesRecv)}`}
          />

          {m.disk?.map((d) => (
            <MetricCard
              key={d.path}
              label={`Disk ${d.path}`}
              value={`${formatGB(d.used)} / ${formatGB(d.total)}  (${d.pct.toFixed(0)}%)`}
            />
          ))}

          {m.topProcs?.length ? (
            <View style={styles.card}>
              <Text style={styles.cardLabel}>TOP PROCESSES (CPU)</Text>
              {m.topProcs.map((p) => (
                <View key={p.pid} style={styles.procRow}>
                  <Text style={styles.procName} numberOfLines={1}>{p.name}</Text>
                  <Text style={styles.procPid}>{p.pid}</Text>
                  <Text style={styles.procCpu}>{p.cpu.toFixed(1)}%</Text>
                </View>
              ))}
            </View>
          ) : null}
        </>
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

function formatGB(b: number): string {
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(0)} KB`;
  if (b < 1024 * 1024 * 1024) return `${(b / 1024 / 1024).toFixed(0)} MB`;
  return `${(b / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#0d0d0d' },
  content:   { padding: 16 },
  center:    { flex: 1, backgroundColor: '#0d0d0d', alignItems: 'center', justifyContent: 'center', padding: 24 },
  notice:    { color: '#aaaaaa', fontSize: 15, textAlign: 'center' },
  headerRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 },
  heading:   { color: '#e0e0e0', fontSize: 20, fontWeight: '700' },
  pauseBtn:  { color: '#4fc3f7', fontSize: 14 },
  waiting:   { color: '#555555', fontSize: 14, textAlign: 'center', marginTop: 40 },
  card:      { backgroundColor: '#111111', borderRadius: 10, padding: 16, marginBottom: 12, borderWidth: 1, borderColor: '#222222' },
  cardLabel: { color: '#777777', fontSize: 12, marginBottom: 6, letterSpacing: 0.8 },
  cardValue: { color: '#e0e0e0', fontSize: 22, fontWeight: '600', fontFamily: 'monospace' },
  procRow:   { flexDirection: 'row', alignItems: 'center', marginTop: 8 },
  procName:  { color: '#e0e0e0', fontSize: 13, flex: 1, fontFamily: 'monospace' },
  procPid:   { color: '#666666', fontSize: 12, width: 64, textAlign: 'right', fontFamily: 'monospace' },
  procCpu:   { color: '#4fc3f7', fontSize: 13, width: 56, textAlign: 'right', fontFamily: 'monospace' },
});
