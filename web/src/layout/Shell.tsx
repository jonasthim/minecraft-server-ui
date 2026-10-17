import { useEffect, useState, type ReactNode } from 'react'
import { AppShell, Burger, Group, Stack, Text, NavLink, Button, Divider, Select, useMantineColorScheme } from '@mantine/core'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../features/auth/AuthProvider'
import { api } from '../api/client'
import { JobActivity } from '../features/jobs'
import type { components } from '../api/schema'
import { startMetricsPoll } from '../features/metrics/poll'
import { notifications } from '@mantine/notifications'
import { useDisclosure } from '@mantine/hooks'
import { IconCube, IconServer, IconUser, IconUsers, IconLock, IconHistory, IconListCheck, IconBell, IconRefresh } from '@tabler/icons-react'

export function Shell({ children }: { children: ReactNode }) {
  const [opened, { toggle, close }] = useDisclosure()
  const { user, logout } = useAuth()
  const { pathname } = useLocation()
  const navigate = useNavigate()
  const { colorScheme, setColorScheme } = useMantineColorScheme()
  const [servers, setServers] = useState<{ scope: string; values: components['schemas']['InstanceDetail'][] }>({ scope: '', values: [] })
  const [serverError, setServerError] = useState('')
  const scope = String(user?.id) + ':' + user?.role
  const currentId = /^\/instances\/([a-z0-9][a-z0-9-]{0,31})(?:\/|$)/.exec(pathname)?.[1]
  useEffect(() => startMetricsPoll(signal => api<{ instances: components['schemas']['InstanceDetail'][] }>('/instances', { signal: AbortSignal.any([signal, AbortSignal.timeout(10000)]) }), data => { setServers({ scope, values: data.instances }); setServerError('') }, () => setServerError('Server list unavailable'), 30000), [scope, pathname])
  const choices = servers.scope === scope ? servers.values : []
  const links = [
    { to: '/', label: 'Servers', icon: IconServer },
    { to: '/catalog', label: 'Content catalogs', icon: IconCube },
    { to: '/jobs', label: 'Jobs', icon: IconListCheck },
    { to: '/account', label: 'Account', icon: IconUser },
    ...(user?.role === 'admin' ? [
      { to: '/users', label: 'Users', icon: IconUsers },
      { to: '/settings/auth', label: 'Authentication', icon: IconLock },
      { to: '/settings/notifications', label: 'Notifications', icon: IconBell },
      { to: '/settings/update', label: 'Manager updates', icon: IconRefresh },
      { to: '/audit', label: 'Audit log', icon: IconHistory },
    ] : []),
  ]
  return <><a className="app-skip-link" href="#main-content">Skip to content</a><AppShell header={{ height: 64 }} navbar={{ width: 232, breakpoint: 'sm', collapsed: { mobile: !opened } }} padding="xl">
    <AppShell.Header><Group h="100%" px="lg" gap="sm">
      <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label={opened ? "Close navigation" : "Open navigation"} aria-expanded={opened} aria-controls="app-navigation" />
      <svg className="app-block-mark" viewBox="0 0 34 34" aria-hidden="true"><path fill="#795438" d="M3 12h28v19H3z"/><path fill="#356c45" d="M3 3h28v12H20v4h-7v-6H3z"/><path fill="#92c29c" d="M3 3h28v4H3z"/><path fill="#a47b54" d="M7 21h5v5H7zm15-1h5v7h-5z"/></svg>
      <Text className="app-brand-name" fw={650} size="lg" style={{ flex: 1 }}>Minecraft Server UI</Text>
      <Select aria-label="Appearance" w={100} size="xs" allowDeselect={false} data={[{ value: 'auto', label: 'System' }, { value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }]} value={colorScheme} onChange={value => { if (value === 'auto' || value === 'light' || value === 'dark') setColorScheme(value) }} />
    </Group></AppShell.Header>
    <AppShell.Navbar p="md" id="app-navigation" className="app-navigation" aria-label="Main navigation"><Stack gap="xs">
      <Select label="Selected server" placeholder="Choose server" searchable value={currentId ?? null} data={choices.map(({ instance }) => ({ value: instance.id, label: instance.name }))} onChange={value => { if (value) { void navigate('/instances/' + encodeURIComponent(value)); close() } }} />
      <JobActivity />
      {serverError && <Text size="xs" c="#f2cda7">{serverError}</Text>}
      {links.map(({ to, label, icon: Icon }) => <NavLink key={to} component={Link} to={to}
        label={label} leftSection={<Icon size={20} />} active={pathname === to} aria-current={pathname === to ? "page" : undefined} onClick={close}
        color="forest" c="#eef3ee" variant="filled" />)}
    </Stack><Stack mt="auto" gap="xs"><Divider color="#46554a" /><Text size="sm" px="sm" style={{ overflowWrap: "anywhere" }}>{user?.display_name || user?.username}</Text><Text size="xs" px="sm" c="#c6d6c8">{user?.role === "admin" ? "Administrator" : user?.role === "operator" ? "Operator" : "Viewer"}</Text>
      <Button className="app-signout" variant="subtle" color="gray" onClick={() => { void logout().catch((error: unknown) => notifications.show({ color: 'red', title: 'Sign out failed', message: error instanceof Error ? error.message : 'Try again' })) }}>Sign out</Button>
    </Stack></AppShell.Navbar>
    <AppShell.Main component="div"><main id="main-content" tabIndex={-1} className="app-content">{children}</main></AppShell.Main>
  </AppShell></>
}
