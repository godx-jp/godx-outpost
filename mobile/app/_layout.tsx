import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { ThemeProvider } from '@react-navigation/native';
import { Tabs } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import React, { useEffect, useRef, useState } from 'react';
import { StyleSheet, View } from 'react-native';
import { ActivityIndicator, PaperProvider, Snackbar, Text } from 'react-native-paper';
import { SafeAreaProvider } from 'react-native-safe-area-context';
import { getActiveHostId, getHost, updateHostTokens } from '../lib/hosts';
import { HostsManager } from '../lib/HostsManager';
import { navTheme, theme } from '../lib/theme';
import { useAuthed, useSwitching } from '../lib/useConn';
import { wsClient } from '../lib/ws';

// Tab bar icons (Ionicons). Colour is supplied by the navigator from navTheme.
type IoniconName = React.ComponentProps<typeof Ionicons>['name'];
const tabIcon = (name: IoniconName) =>
  ({ color, size }: { color: string; size: number }) => (
    <Ionicons name={name} color={color} size={size} />
  );

export default function RootLayout() {
  // Global error surface — nothing fails silently.
  const [err, setErr] = useState<string | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // True until the launch-time reconnect attempt resolves (avoids flashing the
  // login screen before an auto-reconnect to the last host completes).
  const [booting, setBooting] = useState(true);
  const authed = useAuthed();
  const switching = useSwitching();

  useEffect(() => {
    wsClient.onError = (message) => {
      setErr(message);
      if (timerRef.current) clearTimeout(timerRef.current);
      timerRef.current = setTimeout(() => setErr(null), 6000);
    };
    return () => {
      wsClient.onError = null;
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  // Persist refreshed tokens to the active host, and auto-reconnect on launch.
  useEffect(() => {
    wsClient.onTokens = (access, refresh) => {
      const id = wsClient.activeHostId;
      if (id) void updateHostTokens(id, access, refresh);
    };

    let cancelled = false;
    (async () => {
      const aid = await getActiveHostId();
      const h = aid ? await getHost(aid) : undefined;
      if (h && !cancelled) {
        wsClient.setTokens(h.access, h.refresh);
        wsClient.activeHostId = h.id;
        wsClient.activeHostName = h.name;
        await wsClient.resume(h.url);
      }
      if (!cancelled) setBooting(false);
    })();

    return () => {
      cancelled = true;
      wsClient.onTokens = null;
    };
  }, []);

  return (
    <SafeAreaProvider>
      <PaperProvider
        theme={theme}
        settings={{ icon: (props) => <MaterialCommunityIcons {...props} /> }}
      >
        <ThemeProvider value={navTheme}>
          <StatusBar style="light" />
          {booting || switching ? (
            <View style={styles.center}>
              <ActivityIndicator size="large" />
              <Text variant="bodyMedium" style={styles.bootMsg}>
                {switching ? 'Switching host…' : 'Connecting…'}
              </Text>
            </View>
          ) : !authed ? (
            <HostsManager mode="login" />
          ) : (
            <Tabs screenOptions={{ headerShown: false }}>
              <Tabs.Screen name="index"    options={{ href: null }} />
              <Tabs.Screen name="terminal" options={{ title: 'Terminal', tabBarIcon: tabIcon('terminal-outline') }} />
              <Tabs.Screen name="files"    options={{ title: 'Files',    tabBarIcon: tabIcon('folder-outline') }} />
              <Tabs.Screen name="monitor"  options={{ title: 'Monitor',  tabBarIcon: tabIcon('pulse-outline') }} />
              <Tabs.Screen name="more"     options={{ title: 'More',     tabBarIcon: tabIcon('ellipsis-horizontal') }} />
            </Tabs>
          )}
          <Snackbar
            visible={!!err}
            onDismiss={() => setErr(null)}
            duration={6000}
            action={{ label: 'Dismiss', onPress: () => setErr(null) }}
          >
            {err ?? ''}
          </Snackbar>
        </ThemeProvider>
      </PaperProvider>
    </SafeAreaProvider>
  );
}

const styles = StyleSheet.create({
  center:  { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24 },
  bootMsg: { marginTop: 16 },
});
