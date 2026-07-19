import ErrorOutlineIcon from '@mui/icons-material/ErrorOutlined'
import FolderIcon from '@mui/icons-material/Folder'
import InsertDriveFileIcon from '@mui/icons-material/InsertDriveFile'
import { Alert, Box, Chip, Stack, TextField, Typography } from '@mui/material'
import { SimpleTreeView } from '@mui/x-tree-view/SimpleTreeView'
import { TreeItem } from '@mui/x-tree-view/TreeItem'
import { useMemo, useState } from 'react'
import { formatBytes } from '../api'
import type { FileEntry } from '../types'

interface Node {
  id: string
  name: string
  children: Map<string, Node>
  file?: FileEntry
  size: number
}

interface Props {
  files: FileEntry[]
  selected: string[]
  onSelected: (selected: string[]) => void
  readOnly?: boolean
}

export function FileExplorer({ files, selected, onSelected, readOnly = false }: Props) {
  const [search, setSearch] = useState('')
  const roots = useMemo(() => buildTree(files, search), [files, search])
  const missing = files.filter((file) => file.missingReason).length

  return (
    <Stack spacing={2}>
      {missing > 0 && (
        <Alert severity="warning">
          当前版本有 {missing} 个不可恢复文件，仍可恢复其他完整文件。
        </Alert>
      )}
      <TextField
        size="small"
        label="搜索文件名或路径"
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      <Box
        sx={{
          border: 1,
          borderColor: 'divider',
          borderRadius: 2,
          p: 1,
          minHeight: 300,
          maxHeight: 560,
          overflow: 'auto',
          bgcolor: 'background.paper',
        }}
      >
        {roots.length === 0 ? (
          <Typography color="text.secondary" sx={{ p: 2 }}>
            没有文件
          </Typography>
        ) : (
          <SimpleTreeView
            multiSelect
            checkboxSelection={!readOnly}
            selectedItems={selected}
            onSelectedItemsChange={(_, items) => onSelected(Array.isArray(items) ? items : [items])}
          >
            {roots.map(renderNode)}
          </SimpleTreeView>
        )}
      </Box>
    </Stack>
  )
}

function buildTree(files: FileEntry[], search: string): Node[] {
  const root = new Map<string, Node>()
  const needle = search.trim().toLocaleLowerCase()
  for (const file of files) {
    const fullPath = `${file.rootAlias}/${file.relativePath}`
    if (needle && !fullPath.toLocaleLowerCase().includes(needle)) continue
    const parts = fullPath.split('/').filter(Boolean)
    let current = root
    let path = ''
    parts.forEach((part, index) => {
      path = path ? `${path}/${part}` : part
      let node = current.get(part)
      if (!node) {
        node = { id: path, name: part, children: new Map(), size: 0 }
        current.set(part, node)
      }
      node.size += file.size
      if (index === parts.length - 1) node.file = file
      current = node.children
    })
  }
  const sortNodes = (map: Map<string, Node>): Node[] =>
    [...map.values()]
      .map((node) => {
        node.children = new Map(sortNodes(node.children).map((child) => [child.name, child]))
        return node
      })
      .sort(
        (left, right) =>
          Number(Boolean(left.file)) - Number(Boolean(right.file)) ||
          left.name.localeCompare(right.name, 'zh-CN'),
      )
  return sortNodes(root)
}

function renderNode(node: Node) {
  const children = [...node.children.values()]
  const icon = node.file ? (
    node.file.missingReason ? (
      <ErrorOutlineIcon color="warning" fontSize="small" />
    ) : (
      <InsertDriveFileIcon fontSize="small" />
    )
  ) : (
    <FolderIcon color="primary" fontSize="small" />
  )
  const label = (
    <Stack direction="row" spacing={1} sx={{ py: 0.25, alignItems: 'center' }}>
      {icon}
      <Typography variant="body2">{node.name}</Typography>
      <Typography variant="caption" color="text.secondary">
        {formatBytes(node.file?.size ?? node.size)}
      </Typography>
      {node.file?.missingReason && <Chip label="不完整" size="small" color="warning" />}
    </Stack>
  )
  return (
    <TreeItem key={node.id} itemId={node.id} label={label}>
      {children.map(renderNode)}
    </TreeItem>
  )
}
