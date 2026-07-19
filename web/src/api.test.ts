import { ApiError, api, formatBytes } from './api'

describe('api helpers', () => {
  it('formats binary sizes', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1024)).toBe('1.00 KiB')
    expect(formatBytes(1024 * 1024)).toBe('1.00 MiB')
  })

  it('decodes successful JSON responses', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )
    await expect(api<{ ok: boolean }>('/api/test')).resolves.toEqual({ ok: true })
    vi.unstubAllGlobals()
  })

  it('surfaces server error messages', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: '密码错误' }), {
          status: 400,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )
    await expect(api('/api/test')).rejects.toEqual(new ApiError('密码错误', 400))
    vi.unstubAllGlobals()
  })
})
