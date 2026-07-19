import { ThemeProvider } from '@mui/material'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { theme } from '../theme'
import { TaskDialog } from './TaskDialog'

describe('TaskDialog password confirmation', () => {
  it('requires the two new-task passwords to match', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify([]), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )
    const user = userEvent.setup()
    render(
      <ThemeProvider theme={theme}>
        <TaskDialog open onClose={vi.fn()} onSaved={vi.fn()} />
      </ThemeProvider>,
    )
    const save = screen.getByRole('button', { name: '保存' })
    expect((save as HTMLButtonElement).disabled).toBe(true)
    const password = await screen.findByLabelText(/^任务密码/)
    const confirmation = await screen.findByLabelText(/^再次输入任务密码/)
    await user.type(password, 'secret')
    await user.type(confirmation, 'different')
    expect(screen.getByText('两次输入不一致')).toBeTruthy()
    expect((save as HTMLButtonElement).disabled).toBe(true)
    await user.clear(confirmation)
    await user.type(confirmation, 'secret')
    expect((save as HTMLButtonElement).disabled).toBe(false)
    vi.unstubAllGlobals()
  })
})
