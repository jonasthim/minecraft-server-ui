import { createTheme } from '@mantine/core'

export const theme = createTheme({
  primaryColor: 'forest',
  luminanceThreshold: 0.179,
  primaryShade: { light: 7, dark: 4 },
  colors: {
    forest: ['#edf6ee', '#dcebdc', '#b8d7bd', '#92c29c', '#70ad80', '#559565', '#438254', '#356c45', '#2d5b3b', '#254c33'],
  },
  fontFamily: '"Segoe UI", "Noto Sans", sans-serif',
  fontFamilyMonospace: '"Cascadia Code", "DejaVu Sans Mono", monospace',
  headings: { fontFamily: '"Segoe UI", "Noto Sans", sans-serif', fontWeight: '650', sizes: { h1: { fontSize: '1.875rem', lineHeight: '1.25' }, h2: { fontSize: '1.5rem', lineHeight: '1.3' }, h3: { fontSize: '1.25rem', lineHeight: '1.35' } } },
  defaultRadius: 'sm',
  radius: { xs: '2px', sm: '4px', md: '6px', lg: '8px', xl: '12px' },
  cursorType: 'pointer',
  components: {
    Button: { defaultProps: { fw: 600 } },
    Badge: { defaultProps: { tt: 'none', fw: 600, autoContrast: true } },
    Paper: { defaultProps: { shadow: 'none' } },
  },
})
