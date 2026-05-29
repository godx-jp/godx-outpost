import { Tabs } from "expo-router";
import { StatusBar } from "expo-status-bar";
import React from "react";

export default function RootLayout() {
  return (
    <>
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
        <Tabs.Screen
          name="index"
          options={{
            title: "Hosts",
            tabBarLabel: "Hosts",
          }}
        />
        <Tabs.Screen
          name="terminal"
          options={{
            title: "Terminal",
            tabBarLabel: "Terminal",
          }}
        />
        <Tabs.Screen
          name="files"
          options={{
            title: "Files",
            tabBarLabel: "Files",
          }}
        />
        <Tabs.Screen
          name="monitor"
          options={{
            title: "Monitor",
            tabBarLabel: "Monitor",
          }}
        />
        <Tabs.Screen
          name="custom"
          options={{
            title: "Custom",
            tabBarLabel: "Custom",
          }}
        />
      </Tabs>
    </>
  );
}
