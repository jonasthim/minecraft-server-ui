import type { ReactNode } from 'react'
import { AppShell, Burger, Group, Stack, Text, NavLink, Button, Divider } from '@mantine/core'
import { Link, useLocation } from 'react-router-dom'
import { useAuth } from '../features/auth/AuthProvider'
import { notifications } from '@mantine/notifications'
import { useDisclosure } from '@mantine/hooks'
import { IconCube, IconServer, IconUser, IconUsers, IconLock, IconHistory, IconListCheck, IconBell, IconRefresh } from '@tabler/icons-react'

export function Shell({ children }: { children: ReactNode }) {
  const [opened, { toggle, close }] = useDisclosure()
  const { user, logout } = useAuth()
  const { pathname } = useLocation()
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
      { to: '/audit', label: 'Activity', icon: IconHistory },
    ] : []),
  ]
  return <><a className="app-skip-link" href="#main-content">Skip to content</a><AppShell header={{ height: 64 }} navbar={{ width: 232, breakpoint: 'sm', collapsed: { mobile: !opened } }} padding="xl">
    <AppShell.Header><Group h="100%" px="lg" gap="sm">
      <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label={opened ? "Close navigation" : "Open navigation"} aria-expanded={opened} aria-controls="app-navigation" />
      <svg className="app-block-mark" viewBox="0 0 34 34" aria-hidden="true"><path fill="#795438" d="M3 12h28v19H3z"/><path fill="#356c45" d="M3 3h28v12H20v4h-7v-6H3z"/><path fill="#92c29c" d="M3 3h28v4H3z"/><path fill="#a47b54" d="M7 21h5v5H7zm15-1h5v7h-5z"/></svg>
      <Text className="app-brand-name" fw={650} size="lg">Minecraft Server UI</Text>
    </Group></AppShell.Header>
    <AppShell.Navbar p="md" id="app-navigation" className="app-navigation" aria-label="Main navigation"><Stack gap="xs">
      {links.map(({ to, label, icon: Icon }) => <NavLink key={to} component={Link} to={to}
        label={label} leftSection={<Icon size={20} />} active={pathname === to} aria-current={pathname === to ? "page" : undefined} onClick={close}
        color="forest" c="#eef3ee" variant="filled" />)}
    </Stack><Stack mt="auto" gap="xs"><Divider color="#46554a" /><Text size="sm" px="sm" style={{ overflowWrap: "anywhere" }}>{user?.display_name || user?.username}</Text><Text size="xs" px="sm" c="#c6d6c8">{user?.role === "admin" ? "Administrator" : user?.role === "operator" ? "Operator" : "Viewer"}</Text>
      <Button className="app-signout" variant="subtle" color="gray" onClick={() => { void logout().catch((error: unknown) => notifications.show({ color: 'red', title: 'Sign out failed', message: error instanceof Error ? error.message : 'Try again' })) }}>Sign out</Button>
    </Stack></AppShell.Navbar>
    <AppShell.Main component="div"><main id="main-content" tabIndex={-1} className="app-content">{children}</main></AppShell.Main>
  </AppShell></>
}
