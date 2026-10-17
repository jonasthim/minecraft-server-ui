import { Alert, Center, Loader } from '@mantine/core'
import { lazy, Suspense, type ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AuthGate, useAuth } from './features/auth/AuthProvider'
const AccountPage = lazy(() => import('./features/auth/AccountPage').then(m => ({ default: m.AccountPage })))
const UsersPage = lazy(() => import('./features/auth/UsersPage').then(m => ({ default: m.UsersPage })))
const OIDCSettingsPage = lazy(() => import('./features/auth/OIDCSettingsPage').then(m => ({ default: m.OIDCSettingsPage })))
const InstancesPage = lazy(() => import('./features/instances').then(m => ({ default: m.InstancesPage })))
const InstanceLayout = lazy(() => import('./features/instances').then(m => ({ default: m.InstanceLayout })))
const InstancePage = lazy(() => import('./features/instances').then(m => ({ default: m.InstancePage })))
const PlayersPage = lazy(() => import('./features/players/PlayersPage').then(m => ({ default: m.PlayersPage })))
const GameEventsPage = lazy(() => import('./features/game-events/GameEventsPage').then(m => ({ default: m.GameEventsPage })))
const ConsolePage = lazy(() => import('./features/console/ConsolePage').then(m => ({ default: m.ConsolePage })))
const WorldsPage = lazy(() => import('./features/worlds').then(m => ({ default: m.WorldsPage })))
const MapPage = lazy(() => import('./features/map').then(m => ({ default: m.MapPage })))
const MetricsPage = lazy(() => import('./features/metrics/MetricsPanel').then(m => ({ default: m.MetricsPage })))
const NotificationsPage = lazy(() => import('./features/notifications/NotificationsPage').then(m => ({ default: m.NotificationsPage })))
const SelfUpdatePage = lazy(() => import('./features/selfupdate/SelfUpdatePage').then(m => ({ default: m.SelfUpdatePage })))
const ContentPage = lazy(() => import('./features/content').then(m => ({ default: m.ContentPage })))
const ModpacksPage = lazy(() => import('./features/modpacks').then(m => ({ default: m.ModpacksPage })))
const UpdatesPage = lazy(() => import('./features/updates/UpdatesPage').then(m => ({ default: m.UpdatesPage })))
const SchedulesPage = lazy(() => import('./features/schedules').then(m => ({ default: m.SchedulesPage })))
const BackupsPage = lazy(() => import('./features/backups').then(m => ({ default: m.BackupsPage })))
const CatalogPage = lazy(() => import('./features/catalog/CatalogPage').then(m => ({ default: m.CatalogPage })))
const JobsPage = lazy(() => import('./features/jobs').then(m => ({ default: m.JobsPage })))
const AuditPage = lazy(() => import('./features/auth/AuditPage').then(m => ({ default: m.AuditPage })))
import { Shell } from './layout/Shell'

function AdminOnly({ children }: { children: ReactNode }) {
  const { user } = useAuth()
  return user?.role === 'admin' ? children : <Alert color="red">Administrator access required.</Alert>
}

export default function App() {
  return <BrowserRouter><AuthGate><Shell><Suspense fallback={<Center mih={240}><Loader aria-label="Loading page" /></Center>}><Routes>
    <Route path="/" element={<InstancesPage />} />
    <Route path="/instances/:instanceId" element={<InstanceLayout />}>
      <Route index element={<InstancePage />} />
      <Route path="players" element={<PlayersPage />} />
      <Route path="game-events" element={<GameEventsPage />} />
      <Route path="console" element={<ConsolePage />} />
      <Route path="world" element={<WorldsPage />} />
      <Route path="map" element={<MapPage />} />
      <Route path="metrics" element={<MetricsPage />} />
      <Route path="backups" element={<BackupsPage />} />
      <Route path="content" element={<ContentPage />} />
      <Route path="modpack" element={<ModpacksPage />} />
      <Route path="updates" element={<UpdatesPage />} />
      <Route path="schedules" element={<SchedulesPage />} />
    </Route>
    <Route path="/catalog" element={<CatalogPage />} />
    <Route path="/jobs" element={<JobsPage />} />
    <Route path="/account" element={<AccountPage />} />
    <Route path="/users" element={<AdminOnly><UsersPage /></AdminOnly>} />
    <Route path="/settings/auth" element={<AdminOnly><OIDCSettingsPage /></AdminOnly>} />
    <Route path="/settings/notifications" element={<AdminOnly><NotificationsPage /></AdminOnly>} />
    <Route path="/settings/update" element={<AdminOnly><SelfUpdatePage /></AdminOnly>} />
    <Route path="/audit" element={<AdminOnly><AuditPage /></AdminOnly>} />
    <Route path="/login" element={<Navigate to="/" replace />} />
    <Route path="/setup" element={<Navigate to="/" replace />} />
    <Route path="*" element={<Alert color="yellow">Page not found.</Alert>} />
  </Routes></Suspense></Shell></AuthGate></BrowserRouter>
}
