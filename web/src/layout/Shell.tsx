import type { ReactNode } from 'react'
import { AppShell, Burger, Group, Stack, Text, ThemeIcon, Title, NavLink, Button, Divider } from '@mantine/core'
import { Link, useLocation } from 'react-router-dom'
import { useAuth } from '../features/auth/AuthProvider'
import { notifications } from '@mantine/notifications'
import { useDisclosure } from '@mantine/hooks'
import { IconCube, IconServer, IconUser, IconUsers, IconLock, IconHistory } from '@tabler/icons-react'

export function Shell({ children }: { children: ReactNode }) {
  const [opened, { toggle, close }] = useDisclosure()
  const { user, logout } = useAuth()
  const { pathname } = useLocation()
  const links = [
    { to: '/', label: 'Servers', icon: IconServer },
    { to: '/account', label: 'Account', icon: IconUser },
    ...(user?.role === 'admin' ? [
      { to: '/users', label: 'Users', icon: IconUsers },
      { to: '/settings/auth', label: 'Authentication', icon: IconLock },
      { to: '/audit', label: 'Activity', icon: IconHistory },
    ] : []),
  ]
  return <AppShell header={{ height: 64 }} navbar={{ width: 232, breakpoint: 'sm', collapsed: { mobile: !opened } }} padding="xl">
    <AppShell.Header><Group h="100%" px="lg" gap="sm">
      <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label="Toggle navigation" />
      <ThemeIcon color="forest" variant="light" size={34}><IconCube size={22} /></ThemeIcon>
      <Title order={1} size="h4">Minecraft Server UI</Title>
    </Group></AppShell.Header>
    <AppShell.Navbar p="md"><Stack gap="xs">
      {links.map(({ to, label, icon: Icon }) => <NavLink key={to} component={Link} to={to}
        label={label} leftSection={<Icon size={20} />} active={pathname === to} onClick={close}
        color="forest" c="#eef3ee" variant="filled" />)}
    </Stack><Stack mt="auto" gap="xs"><Divider color="#46554a" /><Text size="sm" px="sm">{user?.display_name || user?.username}</Text>
      <Button variant="subtle" color="gray" onClick={() => { void logout().catch((error: unknown) => notifications.show({ color: 'red', title: 'Sign out failed', message: error instanceof Error ? error.message : 'Try again' })) }}>Sign out</Button>
    </Stack></AppShell.Navbar>
    <AppShell.Main><main style={{ maxWidth: 1280, marginInline: 'auto' }}>{children}</main></AppShell.Main>
  </AppShell>
}
