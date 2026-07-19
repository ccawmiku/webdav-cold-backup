import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FileExplorer } from './FileExplorer'
import type { FileEntry } from '../types'

const files: FileEntry[] = [
  {
    id: 'one',
    rootAlias: '照片',
    relativePath: '旅行/a.jpg',
    size: 1024,
    hash: 'a',
    times: { modified: '2026-01-01T00:00:00Z', created: '2026-01-01T00:00:00Z' },
    blocks: [],
  },
  {
    id: 'two',
    rootAlias: '视频',
    relativePath: '电影/b.mp4',
    size: 2048,
    hash: 'b',
    times: { modified: '2026-01-01T00:00:00Z', created: '2026-01-01T00:00:00Z' },
    blocks: [],
    missingReason: '缺少块',
  },
]

describe('FileExplorer', () => {
  it('shows roots and incomplete count', () => {
    render(<FileExplorer files={files} selected={[]} onSelected={() => undefined} />)
    expect(screen.getByText('照片')).toBeTruthy()
    expect(screen.getByText('视频')).toBeTruthy()
    expect(screen.getByText(/1 个不可恢复文件/)).toBeTruthy()
  })

  it('filters the tree by path', async () => {
    const user = userEvent.setup()
    render(<FileExplorer files={files} selected={[]} onSelected={() => undefined} />)
    await user.type(screen.getByLabelText('搜索文件名或路径'), 'a.jpg')
    expect(screen.getByText('照片')).toBeTruthy()
    expect(screen.queryByText('视频')).toBeNull()
  })
})
