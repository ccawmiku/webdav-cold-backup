import { render, screen } from '@testing-library/react'
import { ThemeProvider } from '@mui/material'
import App from './App'
import { theme } from './theme'

describe('App runtime selection', () => {
  it('renders the offline recovery workflow', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({ mode: 'offline', version: 'v1.0.0', platform: 'windows/amd64' }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        ),
    )
    render(
      <ThemeProvider theme={theme}>
        <App />
      </ThemeProvider>,
    )
    expect(await screen.findByText('WebDAV 冷备份恢复')).toBeTruthy()
    expect(screen.getByText(/完全离线运行/)).toBeTruthy()
    vi.unstubAllGlobals()
  })
})
