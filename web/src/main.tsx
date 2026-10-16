import '@mantine/core/styles.css'
import '@mantine/notifications/styles.css'
import './index.css'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { MantineProvider } from '@mantine/core'
import { Notifications } from '@mantine/notifications'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from './features/auth/AuthProvider'
import App from './App'
import { theme } from './theme'

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: 1, staleTime: 5000, refetchOnWindowFocus: false } } })
createRoot(document.getElementById('root')!).render(
  <StrictMode><MantineProvider theme={theme} defaultColorScheme="auto">
    <Notifications position="bottom-right" limit={4} />
    <QueryClientProvider client={queryClient}><AuthProvider><App /></AuthProvider></QueryClientProvider>
  </MantineProvider></StrictMode>,
)
