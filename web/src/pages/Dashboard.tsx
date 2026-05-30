import {
  Badge,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardStat,
  CardTitle,
  EmptyState,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@godxjp/ui/data-display';
import { Input } from '@godxjp/ui/data-entry';
import { DialogConfirm } from '@godxjp/ui/feedback';
import { Button } from '@godxjp/ui/general';
import { Inline, PageContainer, ResponsiveGrid, Stack } from '@godxjp/ui/layout';
import { TabsItems } from '@godxjp/ui/navigation';
import { useQuery } from '@tanstack/react-query';
import { Moon, MonitorDot, RefreshCw, Server, SquareTerminal, Sun, TerminalSquare, Trash2, UserX } from 'lucide-react';
import { useEffect, useState } from 'react';
import {
  fetchState,
  killSession,
  refreshCode,
  renameDevice,
  revokeDevice,
  setDomain,
  termHref,
  type DeviceRow,
  type SessionRow,
} from '../api';
import { applyDark, isDark } from '../theme';

type Confirm = { title: string; description?: string; run: () => Promise<unknown> };

function ThemeToggle() {
  const [dark, setDark] = useState(isDark());
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={() => {
        const next = !dark;
        applyDark(next);
        setDark(next);
      }}
    >
      {dark ? <Sun size={16} /> : <Moon size={16} />}
    </Button>
  );
}

const KIND_ICON: Record<string, typeof Server> = {
  shell: SquareTerminal,
  tmux: TerminalSquare,
  zellij: MonitorDot,
};

export function Dashboard() {
  const { data, refetch } = useQuery({ queryKey: ['state'], queryFn: fetchState, refetchInterval: 3000 });
  const [target, setTarget] = useState('');
  const [domainInput, setDomainInput] = useState('');
  const [confirm, setConfirm] = useState<Confirm | null>(null);

  useEffect(() => {
    if (!data) return;
    setDomainInput(data.domain);
    setTarget((cur) => (data.targets.some((t) => t.url === cur) ? cur : data.targets[0]?.url ?? ''));
  }, [data]);

  if (!data) {
    return (
      <PageContainer title="Outpost">
        <Stack gap="md">Loading…</Stack>
      </PageContainer>
    );
  }

  const selected = data.targets.find((t) => t.url === target) ?? data.targets[0];
  const qrSrc = selected ? `/qr.png?url=${encodeURIComponent(selected.url)}&t=${data.code}` : '';
  const activeDevices = data.devices.filter((d) => d.status === 'active');
  const revokedDevices = data.devices.filter((d) => d.status !== 'active');
  const muxCount = data.sessions.filter((s) => s.kind !== 'shell').length;
  const ask = (c: Confirm) => setConfirm(c);

  return (
    <PageContainer
      title="Outpost"
      subtitle={`device ${data.deviceId.slice(0, 12)}`}
      variant="narrow"
      density="compact"
      extra={
        <Inline gap="sm">
          <Badge variant="success">online</Badge>
          <ThemeToggle />
        </Inline>
      }
    >
      <Stack gap="lg">
        <ResponsiveGrid columns={3}>
          <CardStat label="Sessions" value={data.sessions.length} hint="shells + multiplexers" />
          <CardStat label="Multiplexers" value={muxCount} hint="tmux / zellij" />
          <CardStat label="Paired devices" value={activeDevices.length} hint={`${revokedDevices.length} revoked`} />
        </ResponsiveGrid>

        <Card>
          <CardHeader>
            <CardTitle>Pair a device</CardTitle>
            <CardDescription>Open the Outpost app → Hosts → Scan QR.</CardDescription>
          </CardHeader>
          <CardContent>
            <Inline gap="lg">
              <Stack gap="sm">
                <Inline gap="xs">
                  {data.targets.map((t) => (
                    <Button
                      key={t.url}
                      size="sm"
                      variant={t.url === selected?.url ? 'default' : 'outline'}
                      onClick={() => setTarget(t.url)}
                    >
                      {t.label}
                    </Button>
                  ))}
                </Inline>
                {qrSrc ? <img src={qrSrc} alt="pairing QR" width={200} height={200} /> : null}
                {selected ? (
                  <Inline gap="xs">
                    <Badge variant="secondary">{selected.url}</Badge>
                  </Inline>
                ) : null}
              </Stack>

              <Stack gap="md">
                <CardStat label="Pairing code" value={data.code} hint="rotates every minute" />
                <Inline gap="sm">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={async () => {
                      await refreshCode();
                      refetch();
                    }}
                  >
                    <RefreshCw size={15} /> New code
                  </Button>
                </Inline>
                <Inline gap="sm">
                  <Input
                    placeholder="custom domain (Cloudflare tunnel)"
                    value={domainInput}
                    onChange={(e) => setDomainInput(e.target.value)}
                  />
                  <Button
                    size="sm"
                    onClick={async () => {
                      await setDomain(domainInput.trim());
                      refetch();
                    }}
                  >
                    Save
                  </Button>
                </Inline>
              </Stack>
            </Inline>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Sessions</CardTitle>
            <CardDescription>Open a terminal in the browser, or kill a session.</CardDescription>
          </CardHeader>
          <CardContent>
            {data.sessions.length === 0 ? (
              <EmptyState icon={SquareTerminal} title="No sessions" description="Create one from the app." />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>kind</TableHead>
                    <TableHead>name</TableHead>
                    <TableHead>folder</TableHead>
                    <TableHead>state</TableHead>
                    <TableHead> </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.sessions.map((s: SessionRow) => {
                    const Icon = KIND_ICON[s.kind] ?? Server;
                    return (
                      <TableRow key={`${s.kind}:${s.id}`}>
                        <TableCell>
                          <Inline gap="xs">
                            <Icon size={15} />
                            <Badge variant="secondary">{s.kind}</Badge>
                          </Inline>
                        </TableCell>
                        <TableCell>{s.title}</TableCell>
                        <TableCell>{s.cwd}</TableCell>
                        <TableCell>
                          <Badge variant={s.alive ? 'success' : 'secondary'}>{s.alive ? 'alive' : 'stopped'}</Badge>
                        </TableCell>
                        <TableCell>
                          <Inline gap="xs">
                            {s.alive ? (
                              <Button size="sm" variant="outline" onClick={() => window.open(termHref(s), '_blank')}>
                                <TerminalSquare size={15} /> open
                              </Button>
                            ) : null}
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() =>
                                ask({
                                  title: `Kill “${s.title}”?`,
                                  description: 'This terminal session will be terminated.',
                                  run: () => killSession(s.kind, s.id),
                                })
                              }
                            >
                              <Trash2 size={15} />
                            </Button>
                          </Inline>
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Paired devices</CardTitle>
          </CardHeader>
          <CardContent>
            <TabsItems
              variant="line"
              items={[
                {
                  key: 'active',
                  label: `Active (${activeDevices.length})`,
                  children: (
                    <DeviceTable
                      rows={activeDevices}
                      onRename={(d) => {
                        const name = window.prompt('Device name:', d.name);
                        if (name !== null) renameDevice(d.clientId, name).then(() => refetch());
                      }}
                      onKick={(d) =>
                        ask({
                          title: `Kick “${d.name || d.clientId}”?`,
                          description: 'The device must re-pair to reconnect.',
                          run: () => revokeDevice(d.clientId),
                        })
                      }
                    />
                  ),
                },
                {
                  key: 'revoked',
                  label: `Revoked (${revokedDevices.length})`,
                  children: <DeviceTable rows={revokedDevices} revoked />,
                },
              ]}
            />
          </CardContent>
        </Card>
      </Stack>

      <DialogConfirm
        open={!!confirm}
        onOpenChange={(o) => {
          if (!o) setConfirm(null);
        }}
        title={confirm?.title ?? ''}
        description={confirm?.description}
        variant="destructive"
        confirmLabel="Confirm"
        onConfirm={async () => {
          await confirm?.run();
          setConfirm(null);
          refetch();
        }}
      />
    </PageContainer>
  );
}

function DeviceTable({
  rows,
  revoked,
  onRename,
  onKick,
}: {
  rows: DeviceRow[];
  revoked?: boolean;
  onRename?: (d: DeviceRow) => void;
  onKick?: (d: DeviceRow) => void;
}) {
  if (rows.length === 0) {
    return <EmptyState icon={Server} title={revoked ? 'No revoked devices' : 'No devices'} />;
  }
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>name</TableHead>
          <TableHead>type</TableHead>
          <TableHead>last seen</TableHead>
          <TableHead> </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((d) => (
          <TableRow key={d.clientId}>
            <TableCell>{d.name || '(unnamed)'}</TableCell>
            <TableCell>{d.type || '—'}</TableCell>
            <TableCell>{d.lastSeen}</TableCell>
            <TableCell>
              {revoked ? (
                <Badge variant="secondary">revoked</Badge>
              ) : (
                <Inline gap="xs">
                  <Button size="sm" variant="outline" onClick={() => onRename?.(d)}>
                    rename
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => onKick?.(d)}>
                    <UserX size={15} />
                  </Button>
                </Inline>
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
