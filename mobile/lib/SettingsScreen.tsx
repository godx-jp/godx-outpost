/**
 * Settings — local app preferences. Currently the default Files directory.
 */
import React, { useEffect, useState } from 'react';
import { StyleSheet, View } from 'react-native';
import { Appbar, HelperText, TextInput } from 'react-native-paper';
import { DEFAULT_FILES_DIR, getDefaultDir, setDefaultDir } from './settings';

export function Settings({ onBack }: { onBack: () => void }) {
  const [dir, setDir] = useState(DEFAULT_FILES_DIR);

  useEffect(() => { void getDefaultDir().then(setDir); }, []);

  const save = async () => {
    await setDefaultDir(dir.trim());
    onBack();
  };

  return (
    <View style={styles.flex}>
      <Appbar.Header mode="small">
        <Appbar.BackAction onPress={onBack} />
        <Appbar.Content title="Settings" />
        <Appbar.Action icon="check" onPress={save} />
      </Appbar.Header>
      <View style={styles.form}>
        <TextInput
          mode="outlined"
          label="Thư mục mặc định (Files)"
          value={dir}
          onChangeText={setDir}
          autoCapitalize="none"
          autoCorrect={false}
          placeholder={DEFAULT_FILES_DIR}
        />
        <HelperText type="info" visible>
          Tab Files mở thư mục này khi vào tab hoặc bấm nút projects trên header.
        </HelperText>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  form: { padding: 16, gap: 4 },
});
