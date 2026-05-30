/**
 * More tab — a hub for everything that isn't a primary tab. Navigates between
 * its own sub-pages with local state (no extra routes):
 *   - Hosts (manage / switch / add / remove)
 *   - Custom Handlers
 *   - Settings
 *   - App / Host info
 *   - Disconnect (drops the connection → the auth gate shows the login screen)
 */
import React, { useState } from 'react';
import { StyleSheet, View } from 'react-native';
import { Appbar, Divider, List } from 'react-native-paper';
import { CustomHandlers } from '../lib/CustomHandlers';
import { HostInfo } from '../lib/HostInfo';
import { HostsManager } from '../lib/HostsManager';
import { Settings } from '../lib/SettingsScreen';
import { wsClient } from '../lib/ws';

type Page = 'menu' | 'hosts' | 'custom' | 'settings' | 'info';

export default function MoreScreen() {
  const [page, setPage] = useState<Page>('menu');
  const back = () => setPage('menu');

  if (page === 'hosts')    return <HostsManager mode="embedded" onBack={back} />;
  if (page === 'custom')   return <CustomHandlers onBack={back} />;
  if (page === 'settings') return <Settings onBack={back} />;
  if (page === 'info')     return <HostInfo onBack={back} />;

  return (
    <View style={styles.flex}>
      <Appbar.Header mode="small">
        <Appbar.Content title="More" subtitle={wsClient.activeHostName ?? undefined} />
      </Appbar.Header>
      <List.Section>
        <List.Item
          title="Hosts"
          description="Quản lý, đổi hoặc thêm host"
          left={(p) => <List.Icon {...p} icon="server" />}
          right={(p) => <List.Icon {...p} icon="chevron-right" />}
          onPress={() => setPage('hosts')}
        />
        <Divider />
        <List.Item
          title="Custom Handlers"
          description="Gọi các API handler của host"
          left={(p) => <List.Icon {...p} icon="code-braces" />}
          right={(p) => <List.Icon {...p} icon="chevron-right" />}
          onPress={() => setPage('custom')}
        />
        <Divider />
        <List.Item
          title="Settings"
          description="Thư mục mặc định, tuỳ chọn"
          left={(p) => <List.Icon {...p} icon="cog" />}
          right={(p) => <List.Icon {...p} icon="chevron-right" />}
          onPress={() => setPage('settings')}
        />
        <Divider />
        <List.Item
          title="App / Host info"
          description="Thông tin kết nối & phiên bản"
          left={(p) => <List.Icon {...p} icon="information-outline" />}
          right={(p) => <List.Icon {...p} icon="chevron-right" />}
          onPress={() => setPage('info')}
        />
        <Divider />
        <List.Item
          title="Disconnect"
          description="Ngắt kết nối host hiện tại"
          left={(p) => <List.Icon {...p} icon="logout" />}
          onPress={() => wsClient.disconnect()}
        />
      </List.Section>
    </View>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
});
