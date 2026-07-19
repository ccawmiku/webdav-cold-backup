import ArrowUpwardIcon from '@mui/icons-material/ArrowUpward'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlined'
import FolderIcon from '@mui/icons-material/Folder'
import FolderOpenIcon from '@mui/icons-material/FolderOpen'
import SaveIcon from '@mui/icons-material/Save'
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  IconButton,
  InputLabel,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { api, body } from '../api'
import type { RemoteDirectory, RemotePreset, WebDAVSelection } from '../types'

interface Props {
  value: WebDAVSelection
  onChange: (value: WebDAVSelection) => void
  disabled?: boolean
}

export function WebDAVTargetPicker({ value, onChange, disabled }: Props) {
  const [presets, setPresets] = useState<RemotePreset[]>([])
  const [presetName, setPresetName] = useState('')
  const [browseOpen, setBrowseOpen] = useState(false)
  const [currentPath, setCurrentPath] = useState('')
  const [directories, setDirectories] = useState<RemoteDirectory[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const loadPresets = useCallback(async () => {
    try {
      setPresets(await api<RemotePreset[]>('/api/remote-presets'))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '预存目标读取失败')
    }
  }, [])

  useEffect(() => {
    void loadPresets()
  }, [loadPresets])

  const updateRemote = (patch: Partial<WebDAVSelection['remote']>) =>
    onChange({ remotePresetId: '', remote: { ...value.remote, ...patch } })

  const selectPreset = (id: string) => {
    if (!id) {
      onChange({
        remotePresetId: '',
        remote: { endpoint: 'http://', root: '', username: '', password: '' },
      })
      return
    }
    const preset = presets.find((item) => item.id === id)
    if (preset) onChange({ remotePresetId: id, remote: preset.remote })
  }

  const browse = async (path: string) => {
    setBusy(true)
    setError('')
    try {
      const items = await api<RemoteDirectory[]>('/api/remotes/browse', {
        method: 'POST',
        ...body({ remote: value.remote, path }),
      })
      setCurrentPath(path)
      setDirectories(items)
      setBrowseOpen(true)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '远端目录读取失败')
    } finally {
      setBusy(false)
    }
  }

  const parentPath = () => {
    const parts = currentPath.split('/').filter(Boolean)
    parts.pop()
    void browse(parts.join('/'))
  }

  const savePreset = async () => {
    if (!presetName.trim()) {
      setError('请填写预存目标名称')
      return
    }
    setBusy(true)
    setError('')
    try {
      const saved = await api<RemotePreset>('/api/remote-presets', {
        method: 'POST',
        ...body({ name: presetName, remote: value.remote }),
      })
      await loadPresets()
      setPresetName('')
      onChange({ remotePresetId: saved.id, remote: saved.remote })
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '预存目标保存失败')
    } finally {
      setBusy(false)
    }
  }

  const deletePreset = async () => {
    if (!value.remotePresetId || !window.confirm('删除这个预存目标？已创建的任务不会受影响。'))
      return
    setBusy(true)
    try {
      await api(`/api/remote-presets/${value.remotePresetId}`, { method: 'DELETE' })
      await loadPresets()
      selectPreset('')
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '预存目标删除失败')
    } finally {
      setBusy(false)
    }
  }

  const selectedPreset = presets.find((item) => item.id === value.remotePresetId)

  return (
    <Stack spacing={2}>
      {error && <Alert severity="error">{error}</Alert>}
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1}>
        <FormControl fullWidth disabled={disabled || busy}>
          <InputLabel>预存WebDAV目标</InputLabel>
          <Select
            label="预存WebDAV目标"
            value={value.remotePresetId}
            onChange={(event) => selectPreset(event.target.value)}
          >
            <MenuItem value="">新建或临时使用</MenuItem>
            {presets.map((preset) => (
              <MenuItem key={preset.id} value={preset.id}>
                {preset.name} · {preset.remote.endpoint}/{preset.remote.root}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        {selectedPreset && (
          <Button
            color="error"
            startIcon={<DeleteOutlineIcon />}
            disabled={disabled || busy}
            onClick={() => void deletePreset()}
          >
            删除预存
          </Button>
        )}
      </Stack>

      {selectedPreset ? (
        <Alert severity="success">
          <Typography sx={{ fontWeight: 700 }}>{selectedPreset.name}</Typography>
          <Typography variant="body2">地址：{selectedPreset.remote.endpoint}</Typography>
          <Typography variant="body2">
            用户名：{selectedPreset.remote.username || '匿名'}
          </Typography>
          <Typography variant="body2">目录：/{selectedPreset.remote.root}</Typography>
          <Typography variant="body2">
            密码：{selectedPreset.hasPassword ? '已保存在NAS本地' : '未设置'}
          </Typography>
        </Alert>
      ) : (
        <>
          <TextField
            required
            label="WebDAV地址"
            value={value.remote.endpoint}
            disabled={disabled}
            onChange={(event) => updateRemote({ endpoint: event.target.value })}
            placeholder="http://nas:5005/webdav"
          />
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
            <TextField
              fullWidth
              label="用户名"
              value={value.remote.username ?? ''}
              disabled={disabled}
              onChange={(event) => updateRemote({ username: event.target.value })}
            />
            <TextField
              fullWidth
              label="WebDAV密码"
              type="password"
              value={value.remote.password ?? ''}
              disabled={disabled}
              onChange={(event) => updateRemote({ password: event.target.value })}
            />
          </Stack>
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1}>
            <TextField
              fullWidth
              label="已选择的远端目录"
              value={value.remote.root}
              slotProps={{ input: { readOnly: true } }}
              helperText="先填写地址和凭据，再浏览并选择位置"
            />
            <Button
              variant="outlined"
              startIcon={<FolderOpenIcon />}
              disabled={disabled || busy || !value.remote.endpoint}
              onClick={() => void browse(value.remote.root)}
            >
              浏览位置
            </Button>
          </Stack>
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1}>
            <TextField
              fullWidth
              label="预存目标名称（可选）"
              value={presetName}
              disabled={disabled}
              onChange={(event) => setPresetName(event.target.value)}
              placeholder="例如：我的主网盘"
            />
            <Button
              startIcon={<SaveIcon />}
              disabled={disabled || busy || !presetName.trim() || !value.remote.endpoint}
              onClick={() => void savePreset()}
            >
              保存供以后使用
            </Button>
          </Stack>
        </>
      )}

      <Dialog open={browseOpen} onClose={() => setBrowseOpen(false)} fullWidth maxWidth="sm">
        <DialogTitle>选择WebDAV目录</DialogTitle>
        <DialogContent>
          <Box sx={{ display: 'flex', gap: 1, alignItems: 'center', mb: 1 }}>
            <IconButton
              aria-label="远端上一级目录"
              disabled={!currentPath || busy}
              onClick={parentPath}
            >
              <ArrowUpwardIcon />
            </IconButton>
            <TextField
              fullWidth
              size="small"
              value={`/${currentPath}`}
              slotProps={{ input: { readOnly: true } }}
            />
          </Box>
          <List
            sx={{
              minHeight: 260,
              maxHeight: 420,
              overflow: 'auto',
              border: 1,
              borderColor: 'divider',
            }}
          >
            {directories.map((directory) => (
              <ListItemButton
                key={directory.path}
                onDoubleClick={() => void browse(directory.path)}
              >
                <ListItemIcon>
                  <FolderIcon color="primary" />
                </ListItemIcon>
                <ListItemText primary={directory.name} secondary={`/${directory.path}`} />
                <Button size="small" onClick={() => void browse(directory.path)}>
                  打开
                </Button>
              </ListItemButton>
            ))}
            {!directories.length && <ListItemText sx={{ p: 2 }} primary="当前目录没有子目录" />}
          </List>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setBrowseOpen(false)}>取消</Button>
          <Button
            variant="contained"
            onClick={() => {
              updateRemote({ root: currentPath })
              setBrowseOpen(false)
            }}
          >
            选择当前目录
          </Button>
        </DialogActions>
      </Dialog>
    </Stack>
  )
}
