import { Alert, Center, Loader, Paper, Stack, Text, ThemeIcon, Title } from '@mantine/core'
import { IconServer } from '@tabler/icons-react'
import { lazy, Suspense, type ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AuthGate, useAuth } from './features/auth/AuthProvider'
const AccountPage = lazy(() => import('./features/auth/AccountPage').then(m => ({ default: m.AccountPage })))
const UsersPage = lazy(() => import('./features/auth/UsersPage').then(m => ({ default: m.UsersPage })))
const OIDCSettingsPage = lazy(() => import('./features/auth/OIDCSettingsPage').then(m => ({ default: m.OIDCSettingsPage })))
const AuditPage = lazy(() => import('./features/auth/AuditPage').then(m => ({ default: m.AuditPage })))
import { Shell } from './layout/Shell'

function AdminOnly({ children }: { children: ReactNode }) {
  const { user } = useAuth()
  return user?.role === 'admin' ? children : <Alert color="red">Administrator access required.</Alert>
}

export default function App() {
  return <BrowserRouter><AuthGate><Shell><Suspense fallback={<Center mih={240}><Loader aria-label="Loading page" /></Center>}><Routes>
    <Route path="/" element={<ServersPage />} />
    <Route path="/account" element={<AccountPage />} />
    <Route path="/users" element={<AdminOnly><UsersPage /></AdminOnly>} />
    <Route path="/settings/auth" element={<AdminOnly><OIDCSettingsPage /></AdminOnly>} />
    <Route path="/audit" element={<AdminOnly><AuditPage /></AdminOnly>} />
    <Route path="/login" element={<Navigate to="/" replace />} />
    <Route path="/setup" element={<Navigate to="/" replace />} />
    <Route path="*" element={<Alert color="yellow">Page not found.</Alert>} />
  </Routes></Suspense></Shell></AuthGate></BrowserRouter>
}

function ServersPage() {
  return <Stack gap="xl">
    <div><Title order={2}>Servers</Title><Text c="dimmed" mt={4}>Your Minecraft servers, in one place.</Text></div>
    <Paper withBorder p="xl"><Center mih={260}><Stack align="center" gap="sm" maw={420}>
      <ThemeIcon size={56} variant="light" color="forest"><IconServer size={30} /></ThemeIcon>
      <Title order={3}>No servers configured</Title>
      <Text c="dimmed" ta="center">Create a separate home for each world, with its own Minecraft version, settings, and players.</Text>
    </Stack></Center></Paper>
  </Stack>
}
