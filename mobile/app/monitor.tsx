/**
 * Monitor screen – subscribes to the "sys" channel via wsClient.
 *
 * Sends:  { ch: Ch.Sys, type: 'subscribe',   data: { intervalMs } }  (on mount)
 *         { ch: Ch.Sys, type: 'unsubscribe' }                        (on unmount)
 * Reads:  { ch: Ch.Sys, type: 'metrics', data: Metrics }  (pushed by the server)
 *
 * The metric shape mirrors internal/sys/sys.go exactly. Metrics are per-host:
 * the active WebSocket targets one host, so on a host switch (Hosts tab) we
 * clear and re-subscribe on focus. The header shows which host is shown.
 *
 * UI: react-native-paper components; colour from the theme; StyleSheet holds
 * layout only.
 */

import { useFocusEffect } from 'expo-router';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ScrollView, StyleSheet, View } from 'react-native';
import {
  ActivityIndicator, Appbar, Card, Divider, ProgressBar, Text, useTheme,
} from 'react-native-paper';
import { Ch, type Envelope } from '../lib/protocol';
import { type AppTheme, usageColor } from '../lib/theme';
import { useActiveHostName, useAuthed } from '../lib/useConn';
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
  const authed = useAuthed();
  const host = useActiveHostName() ?? 'host';
  const [m, setM]           = useState<Metrics | null>(null);
  const [net, setNet]       = useState<{ up: number; down: number }>({ up: 0, down: 0 });
  const [paused, setPaused] = useState(false);
  const pausedRef           = useRef(paused);
  pausedRef.current         = paused;
  const lastHostRef         = useRef<string | null>(wsClient.activeHostId);
  // Previous cumulative net counters, used to derive a per-second rate.
  const prevNetRef          = useRef<{ sent: number; recv: number; ts: number } | null>(null);

  useEffect(() => {
    if (!authed) return;

    const prevOnEnvelope = wsClient.onEnvelope;
    wsClient.onEnvelope = (env: Envelope) => {
      prevOnEnvelope?.(env);
      if (env.ch !== Ch.Sys || env.type !== 'metrics') return;
      if (pausedRef.current || !env.data) return;

      const data = env.data as Metrics;
      setM(data);

      const prev = prevNetRef.current;
      if (prev) {
        const dt = Math.max(0.5, (data.ts - prev.ts) / 1000);
        setNet({
          up: Math.max(0, (data.net.bytesSent - prev.sent) / dt),
          down: Math.max(0, (data.net.bytesRecv - prev.recv) / dt),
        });
      }
      prevNetRef.current = { sent: data.net.bytesSent, recv: data.net.bytesRecv, ts: data.ts };
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
  }, [authed]);

  // When the active host changed (switched on the Hosts tab), clear and
  // re-subscribe on the new host's connection upon focus.
  useFocusEffect(
    useCallback(() => {
      if (wsClient.activeHostId !== lastHostRef.current) {
        lastHostRef.current = wsClient.activeHostId;
        setM(null);
        setNet({ up: 0, down: 0 });
        prevNetRef.current = null;
      }
      if (wsClient.isConnected) {
        try {
          wsClient.send({ ch: Ch.Sys, type: 'subscribe', data: { intervalMs: SUBSCRIBE_INTERVAL_MS } });
        } catch {
          /* not connected yet */
        }
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
        <Appbar.Content title="Monitor" subtitle={host} />
        <Appbar.Action icon={paused ? 'play' : 'pause'} onPress={() => setPaused((p) => !p)} />
      </Appbar.Header>

      {!m ? (
        <View style={styles.center}>
          <ActivityIndicator />
          <Text variant="bodyMedium" style={styles.waiting}>Waiting for data…</Text>
        </View>
      ) : (
        <ScrollView contentContainerStyle={styles.content}>
          <View style={styles.grid}>
            <StatCard label="CPU" value={`${m.cpuPct.toFixed(0)}%`} pct={m.cpuPct} />
            <StatCard
              label="RAM"
              value={`${m.mem.pct.toFixed(0)}%`}
              detail={`${formatBytes(m.mem.used)} / ${formatBytes(m.mem.total)}`}
              pct={m.mem.pct}
            />
            {m.swap && m.swap.total > 0 ? (
              <StatCard
                label="Swap"
                value={`${m.swap.pct.toFixed(0)}%`}
                detail={`${formatBytes(m.swap.used)} / ${formatBytes(m.swap.total)}`}
                pct={m.swap.pct}
              />
            ) : null}
            {m.disk?.map((d) => (
              <StatCard
                key={d.path}
                label={`Disk ${d.path}`}
                value={`${d.pct.toFixed(0)}%`}
                detail={`${formatBytes(d.used)} / ${formatBytes(d.total)}`}
                pct={d.pct}
              />
            ))}

            <Card style={styles.gridItem} mode="contained">
              <Card.Content style={styles.tight}>
                <Text variant="labelMedium">NETWORK</Text>
                <Text variant="bodyLarge">↓ {formatRate(net.down)}</Text>
                <Text variant="bodyLarge">↑ {formatRate(net.up)}</Text>
              </Card.Content>
            </Card>
            <Card style={styles.gridItem} mode="contained">
              <Card.Content style={styles.tight}>
                <Text variant="labelMedium">LOAD AVG</Text>
                {m.load ? (
                  <>
                    <Text variant="bodyLarge">{m.load.load1.toFixed(2)}</Text>
                    <Text variant="bodySmall">
                      {m.load.load5.toFixed(2)}  ·  {m.load.load15.toFixed(2)}
                    </Text>
                  </>
                ) : (
                  <Text variant="bodySmall">n/a</Text>
                )}
              </Card.Content>
            </Card>
          </View>

          {m.topProcs?.length ? (
            <Card mode="contained">
              <Card.Content style={styles.tight}>
                <Text variant="labelMedium" style={styles.procHeader}>TOP PROCESSES · CPU</Text>
                {m.topProcs.map((p, i) => (
                  <View key={p.pid}>
                    {i > 0 ? <Divider /> : null}
                    <ProcRow proc={p} />
                  </View>
                ))}
              </Card.Content>
            </Card>
          ) : null}
        </ScrollView>
      )}
    </View>
  );
}

/* ── Sub-components ─────────────────────────────────────────────── */

function StatCard({
  label, value, detail, pct,
}: { label: string; value: string; detail?: string; pct: number }) {
  const theme = useTheme<AppTheme>();
  return (
    <Card style={styles.gridItem} mode="contained">
      <Card.Content style={styles.tight}>
        <Text variant="labelMedium">{label}</Text>
        <Text variant="titleMedium">{value}</Text>
        {detail ? <Text variant="bodySmall">{detail}</Text> : null}
        <ProgressBar
          progress={Math.min(1, Math.max(0, pct / 100))}
          color={usageColor(theme.colors, pct)}
          style={styles.bar}
        />
      </Card.Content>
    </Card>
  );
}

function ProcRow({ proc }: { proc: ProcStat }) {
  const theme = useTheme<AppTheme>();
  return (
    <View style={styles.procRow}>
      <View style={styles.procInfo}>
        <Text variant="bodyMedium" numberOfLines={1}>{proc.name}</Text>
        <Text variant="bodySmall">{proc.pid}</Text>
      </View>
      <View style={styles.procBar}>
        <ProgressBar
          progress={Math.min(1, Math.max(0, proc.cpu / 100))}
          color={usageColor(theme.colors, proc.cpu)}
          style={styles.bar}
        />
      </View>
      <Text variant="bodyMedium" style={styles.procCpu}>{proc.cpu.toFixed(0)}%</Text>
    </View>
  );
}

/* ── Helpers ────────────────────────────────────────────────────── */

function formatBytes(b: number): string {
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(0)} KB`;
  if (b < 1024 * 1024 * 1024) return `${(b / 1024 / 1024).toFixed(0)} MB`;
  return `${(b / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

function formatRate(bytesPerSec: number): string {
  if (bytesPerSec < 1024) return `${bytesPerSec.toFixed(0)} B/s`;
  if (bytesPerSec < 1024 * 1024) return `${(bytesPerSec / 1024).toFixed(0)} KB/s`;
  return `${(bytesPerSec / 1024 / 1024).toFixed(1)} MB/s`;
}

/* ── Layout only (no colour) ────────────────────────────────────── */

const styles = StyleSheet.create({
  flex:       { flex: 1 },
  center:     { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24 },
  waiting:    { marginTop: 12 },
  content:    { padding: 8, gap: 8 },
  grid:       { flexDirection: 'row', flexWrap: 'wrap', gap: 8 },
  // Paper's Surface only hoists `width` (not `flexBasis`) to its outer layout
  // layer, so two-column sizing must use width.
  gridItem:   { width: '48%' },
  tight:      { paddingVertical: 10, gap: 2 },
  bar:        { height: 6, borderRadius: 3, marginTop: 6 },
  procHeader: { marginBottom: 2 },
  procRow:    { flexDirection: 'row', alignItems: 'center', paddingVertical: 7, gap: 10 },
  procInfo:   { flex: 1 },
  procBar:    { width: 72, justifyContent: 'center' },
  procCpu:    { width: 40, textAlign: 'right' },
});
