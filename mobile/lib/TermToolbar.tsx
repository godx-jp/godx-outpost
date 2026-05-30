/**
 * TermToolbar — key bar shown above the keyboard in the Terminal screen.
 *
 * Left: a horizontally-scrollable row of common keys/combos.
 * Right (pinned): an arrow-pad button (pops up a 4-way d-pad, Termius-style) and
 * a compose button — tap it to type a whole command locally on the phone
 * keyboard (with autocomplete/editing) and Send the entire line at once.
 *
 * All glyphs are vector icons (MaterialCommunityIcons), no emoji.
 */
import { MaterialCommunityIcons } from '@expo/vector-icons';
import { useBottomTabBarHeight } from '@react-navigation/bottom-tabs';
import React, { useEffect, useState } from 'react';
import {
  Keyboard, Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View,
} from 'react-native';

const KEYS: { label: string; bytes: number[] }[] = [
  { label: 'esc', bytes: [0x1b] },
  { label: 'tab', bytes: [0x09] },
  { label: '^C', bytes: [0x03] },
  { label: '^D', bytes: [0x04] },
  { label: '^Z', bytes: [0x1a] },
  { label: '^L', bytes: [0x0c] },
  { label: '^R', bytes: [0x12] },
  { label: '^A', bytes: [0x01] },
  { label: '^E', bytes: [0x05] },
  { label: '^U', bytes: [0x15] },
  { label: '^K', bytes: [0x0b] },
  { label: '^W', bytes: [0x17] },
  { label: '|', bytes: [0x7c] },
  { label: '~', bytes: [0x7e] },
  { label: '/', bytes: [0x2f] },
  { label: '-', bytes: [0x2d] },
  { label: '_', bytes: [0x5f] },
  { label: '*', bytes: [0x2a] },
  { label: '$', bytes: [0x24] },
  { label: '&', bytes: [0x26] },
  { label: ':', bytes: [0x3a] },
  { label: 'home', bytes: [0x1b, 0x5b, 0x48] },
  { label: 'end', bytes: [0x1b, 0x5b, 0x46] },
];

const UP = [0x1b, 0x5b, 0x41];
const DOWN = [0x1b, 0x5b, 0x42];
const RIGHT = [0x1b, 0x5b, 0x43];
const LEFT = [0x1b, 0x5b, 0x44];
const CR = 0x0d;

function strBytes(s: string): number[] {
  return Array.from(new TextEncoder().encode(s));
}

export function TermToolbar({ onKey }: { onKey: (bytes: number[]) => void }) {
  const [kb, setKb] = useState(0);
  const [pad, setPad] = useState(false);
  const [compose, setCompose] = useState(false);
  const [draft, setDraft] = useState('');
  const tabBar = useBottomTabBarHeight();

  useEffect(() => {
    const showEvt = Platform.OS === 'ios' ? 'keyboardWillShow' : 'keyboardDidShow';
    const hideEvt = Platform.OS === 'ios' ? 'keyboardWillHide' : 'keyboardDidHide';
    const s = Keyboard.addListener(showEvt, (e) => setKb(e.endCoordinates?.height ?? 0));
    const h = Keyboard.addListener(hideEvt, () => setKb(0));
    return () => {
      s.remove();
      h.remove();
    };
  }, []);

  const lift = Math.max(0, kb - tabBar);

  const send = () => {
    if (draft.length > 0) onKey([...strBytes(draft), CR]); // send the whole line + run
    setDraft('');
    setCompose(false);
  };

  return (
    <View style={[styles.wrap, { marginBottom: lift }]}>
      {compose ? (
        <View style={styles.composeRow}>
          <Pressable style={styles.iconBtn} onPress={() => { setCompose(false); setDraft(''); }}>
            <MaterialCommunityIcons name="close" size={22} color="#aaa" />
          </Pressable>
          <TextInput
            style={styles.input}
            value={draft}
            onChangeText={setDraft}
            placeholder="Type a command, then send…"
            placeholderTextColor="#666"
            autoFocus
            autoCapitalize="none"
            autoCorrect={false}
            blurOnSubmit={false}
            returnKeyType="send"
            onSubmitEditing={send}
          />
          <Pressable style={[styles.iconBtn, styles.sendBtn]} onPress={send}>
            <MaterialCommunityIcons name="send" size={20} color="#0d0d0d" />
          </Pressable>
        </View>
      ) : (
        <>
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            keyboardShouldPersistTaps="always"
            style={styles.scroll}
            contentContainerStyle={styles.content}
          >
            {KEYS.map((k, i) => (
              <Pressable
                key={i}
                style={({ pressed }) => [styles.key, pressed && styles.pressed]}
                onPress={() => onKey(k.bytes)}
              >
                <Text style={styles.keyText}>{k.label}</Text>
              </Pressable>
            ))}
          </ScrollView>

          {/* Compose button */}
          <Pressable
            style={({ pressed }) => [styles.key, styles.rightBtn, pressed && styles.pressed]}
            onPress={() => { setPad(false); setCompose(true); }}
          >
            <MaterialCommunityIcons name="message-text-outline" size={22} color="#e0e0e0" />
          </Pressable>

          {/* Arrow-pad button */}
          <Pressable
            style={({ pressed }) => [styles.key, styles.rightBtn, (pressed || pad) && styles.pressed]}
            onPress={() => setPad((o) => !o)}
          >
            <MaterialCommunityIcons name={pad ? 'close' : 'arrow-all'} size={22} color="#e0e0e0" />
          </Pressable>

          {pad ? (
            <View style={styles.dpad}>
              <Pressable style={[styles.dkey, styles.dUp]} onPress={() => onKey(UP)}>
                <MaterialCommunityIcons name="chevron-up" size={26} color="#e0e0e0" />
              </Pressable>
              <Pressable style={[styles.dkey, styles.dLeft]} onPress={() => onKey(LEFT)}>
                <MaterialCommunityIcons name="chevron-left" size={26} color="#e0e0e0" />
              </Pressable>
              <Pressable style={[styles.dkey, styles.dRight]} onPress={() => onKey(RIGHT)}>
                <MaterialCommunityIcons name="chevron-right" size={26} color="#e0e0e0" />
              </Pressable>
              <Pressable style={[styles.dkey, styles.dDown]} onPress={() => onKey(DOWN)}>
                <MaterialCommunityIcons name="chevron-down" size={26} color="#e0e0e0" />
              </Pressable>
            </View>
          ) : null}
        </>
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: {
    flexDirection: 'row', alignItems: 'center',
    backgroundColor: '#161616', borderTopWidth: 1, borderTopColor: '#262626',
  },
  scroll: { flex: 1 },
  content: { paddingHorizontal: 6, paddingVertical: 6, gap: 6, alignItems: 'center' },
  key: {
    minWidth: 40, paddingHorizontal: 12, paddingVertical: 8, borderRadius: 8,
    backgroundColor: '#262626', alignItems: 'center', justifyContent: 'center',
  },
  pressed: { backgroundColor: '#3a3a3a' },
  keyText: { color: '#e0e0e0', fontSize: 15, fontFamily: 'monospace' },
  rightBtn: { marginVertical: 6, marginRight: 6, minWidth: 44 },

  // Compose mode
  composeRow: { flexDirection: 'row', alignItems: 'center', padding: 6, gap: 6, flex: 1 },
  iconBtn: { width: 40, height: 40, borderRadius: 8, alignItems: 'center', justifyContent: 'center' },
  sendBtn: { backgroundColor: '#4fc3f7' },
  input: {
    flex: 1, height: 40, color: '#e0e0e0', fontSize: 16, fontFamily: 'monospace',
    backgroundColor: '#0d0d0d', borderRadius: 8, paddingHorizontal: 12, borderWidth: 1, borderColor: '#2a2a2a',
  },

  // Floating d-pad
  dpad: { position: 'absolute', right: 8, bottom: 56, width: 138, height: 138 },
  dkey: {
    position: 'absolute', width: 44, height: 44, borderRadius: 10,
    backgroundColor: '#2e2e2e', borderWidth: 1, borderColor: '#3a3a3a',
    alignItems: 'center', justifyContent: 'center',
  },
  dUp: { top: 0, left: 47 },
  dDown: { bottom: 0, left: 47 },
  dLeft: { left: 0, top: 47 },
  dRight: { right: 0, top: 47 },
});
