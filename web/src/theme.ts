import { createTheme } from '@mui/material/styles'

export const theme = createTheme({
  colorSchemes: { light: true, dark: true },
  palette: {
    primary: { main: '#0f766e' },
    secondary: { main: '#2563eb' },
    background: { default: '#f4f7f7', paper: '#ffffff' },
  },
  shape: { borderRadius: 12 },
  typography: {
    fontFamily:
      'Inter, "Noto Sans SC", "Microsoft YaHei", system-ui, -apple-system, BlinkMacSystemFont, sans-serif',
    h4: { fontWeight: 750 },
    h5: { fontWeight: 700 },
    h6: { fontWeight: 700 },
  },
  components: {
    MuiButton: { defaultProps: { disableElevation: true } },
    MuiCard: { styleOverrides: { root: { border: '1px solid rgba(100,116,139,.16)' } } },
  },
})
