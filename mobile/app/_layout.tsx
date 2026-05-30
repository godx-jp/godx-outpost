import { Tabs } from "expo-router";
import { StatusBar } from "expo-status-bar";
import React, { useEffect, useRef, useState } from "react";
import { Platform, StyleSheet, Text, TouchableOpacity, View } from "react-native";
import { wsClient } from "../lib/ws";

export default function RootLayout() {
  // Global error banner — nothing fails silently. Any unsolicited server error
  // envelope or transport problem surfaces here.
  const [err, setErr] = useState<string | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

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

  return (
    <View style={{ flex: 1, backgroundColor: "#0d0d0d" }}>
      <StatusBar style="light" />
      <Tabs
        screenOptions={{
          headerStyle: { backgroundColor: "#0d0d0d" },
          headerTintColor: "#e0e0e0",
          tabBarStyle: { backgroundColor: "#111111", borderTopColor: "#222222" },
          tabBarActiveTintColor: "#4fc3f7",
          tabBarInactiveTintColor: "#555555",
        }}
      >
        <Tabs.Screen name="index" options={{ title: "Hosts", tabBarLabel: "Hosts" }} />
        <Tabs.Screen name="terminal" options={{ title: "Terminal", tabBarLabel: "Terminal" }} />
        <Tabs.Screen name="files" options={{ title: "Files", tabBarLabel: "Files" }} />
        <Tabs.Screen name="monitor" options={{ title: "Monitor", tabBarLabel: "Monitor" }} />
        <Tabs.Screen name="custom" options={{ title: "Custom", tabBarLabel: "Custom" }} />
      </Tabs>

      {err ? (
        <TouchableOpacity
          style={styles.banner}
          activeOpacity={0.9}
          onPress={() => setErr(null)}
        >
          <Text style={styles.bannerLabel}>⚠︎ Error</Text>
          <Text style={styles.bannerText} numberOfLines={3}>{err}</Text>
          <Text style={styles.bannerDismiss}>tap to dismiss</Text>
        </TouchableOpacity>
      ) : null}
    </View>
  );
}

const styles = StyleSheet.create({
  banner: {
    position: "absolute",
    top: Platform.OS === "ios" ? 58 : 24,
    left: 12,
    right: 12,
    backgroundColor: "#3a1414",
    borderColor: "#ef5350",
    borderWidth: 1,
    borderRadius: 10,
    padding: 12,
  },
  bannerLabel:   { color: "#ef5350", fontSize: 12, fontWeight: "700", marginBottom: 2 },
  bannerText:    { color: "#f5d2d2", fontSize: 13 },
  bannerDismiss: { color: "#a06a6a", fontSize: 11, marginTop: 4 },
});
