import ArrowUpwardIcon from '@mui/icons-material/ArrowUpward'
import FolderIcon from '@mui/icons-material/Folder'
import FolderOpenIcon from '@mui/icons-material/FolderOpen'
import {
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  TextField,
  Tooltip,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { api } from '../api'
import type { FsItem } from '../types'

interface Props {
  label: string
  value: string
  onChange: (value: string) => void
  helperText?: string
}

export function DirectoryPicker({ label, value, onChange, helperText }: Props) {
  const [open, setOpen] = useState(false)
  const [current, setCurrent] = useState('')
  const [items, setItems] = useState<FsItem[]>([])
  const [error, setError] = useState('')

  const load = async (path = '') => {
    try {
      setError('')
      const result = await api<FsItem[]>(
        `/api/fs${path ? `?path=${encodeURIComponent(path)}` : ''}`,
      )
      setCurrent(path)
      setItems(result)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '目录读取失败')
    }
  }

  useEffect(() => {
    if (open) void load(value)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const parent = () => {
    const normalized = current.replace(/[\\/]+$/, '')
    const separator = normalized.includes('\\') ? '\\' : '/'
    const index = normalized.lastIndexOf(separator)
    if (index <= 0) void load('')
    else void load(normalized.slice(0, index) || separator)
  }

  return (
    <>
      <Box sx={{ display: 'flex', gap: 1, alignItems: 'flex-start' }}>
        <TextField
          fullWidth
          label={label}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          helperText={helperText}
        />
        <Button
          variant="outlined"
          startIcon={<FolderOpenIcon />}
          onClick={() => setOpen(true)}
          sx={{ mt: 0.5 }}
        >
          浏览
        </Button>
      </Box>
      <Dialog open={open} onClose={() => setOpen(false)} fullWidth maxWidth="sm">
        <DialogTitle>选择目录</DialogTitle>
        <DialogContent>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
            <Tooltip title="上一级">
              <span>
                <IconButton onClick={parent} disabled={!current} aria-label="上一级目录">
                  <ArrowUpwardIcon />
                </IconButton>
              </span>
            </Tooltip>
            <TextField
              size="small"
              fullWidth
              value={current || '可用根目录'}
              slotProps={{ input: { readOnly: true } }}
            />
          </Box>
          {error && <Box color="error.main">{error}</Box>}
          <List
            dense
            sx={{
              minHeight: 280,
              maxHeight: 420,
              overflow: 'auto',
              border: 1,
              borderColor: 'divider',
              borderRadius: 1,
            }}
          >
            {items.map((item) => (
              <ListItemButton
                key={item.path}
                onDoubleClick={() => void load(item.path)}
                onClick={() => setCurrent(item.path)}
                selected={current === item.path}
              >
                <ListItemIcon>
                  <FolderIcon color="primary" />
                </ListItemIcon>
                <ListItemText primary={item.name} secondary={item.path} />
              </ListItemButton>
            ))}
          </List>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setOpen(false)}>取消</Button>
          <Button
            disabled={!current}
            variant="contained"
            onClick={() => {
              onChange(current)
              setOpen(false)
            }}
          >
            选择当前目录
          </Button>
        </DialogActions>
      </Dialog>
    </>
  )
}
