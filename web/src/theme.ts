import { createTheme } from '@mantine/core'

export const theme = createTheme({
  primaryColor: 'forest',
  colors: {
    forest: ['#eff7f0', '#dfebdf', '#bfd6bf', '#9cbe9d', '#7ca97f', '#659b69', '#58935e', '#477e4e', '#3c7043', '#2e6036'],
  },
  fontFamily: '"Segoe UI", "Noto Sans", sans-serif',
  fontFamilyMonospace: '"Cascadia Code", "DejaVu Sans Mono", monospace',
  headings: { fontFamily: '"Segoe UI", "Noto Sans", sans-serif', fontWeight: '650' },
  defaultRadius: 'sm',
  cursorType: 'pointer',
})
