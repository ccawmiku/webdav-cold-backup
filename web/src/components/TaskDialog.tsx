import AddIcon from '@mui/icons-material/Add'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlined'
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  FormControlLabel,
  IconButton,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { api, body } from '../api'
import type { Schedule, SourceRoot, Task, TaskMode } from '../types'
import { DirectoryPicker } from './DirectoryPicker'
import { ScheduleFields } from './ScheduleFields'

interface Props {
  open: boolean
  onClose: () => void
  onSaved: (task: Task, runNow: boolean) => void
  task?: Task
}

const initialSchedule: Schedule = { type: 'manual', weekday: 0, hour: 0, minute: 0 }

export function TaskDialog({ open, onClose, onSaved, task }: Props) {
  const [name, setName] = useState('')
  const [mode, setMode] = useState<TaskMode>('snapshot')
  const [password, setPassword] = useState('')
  const [sources, setSources] = useState<SourceRoot[]>([{ path: '', alias: '' }])
  const [endpoint, setEndpoint] = useState('http://')
  const [remoteRoot, setRemoteRoot] = useState('backup')
  const [username, setUsername] = useState('')
  const [remotePassword, setRemotePassword] = useState('')
  const [blockSize, setBlockSize] = useState(1_000_000_000)
  const [retention, setRetention] = useState(3)
  const [schedule, setSchedule] = useState<Schedule>(initialSchedule)
  const [runNow, setRunNow] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setName(task?.name ?? '')
    setMode(task?.mode ?? 'snapshot')
    setSources(task?.sources?.length ? task.sources : [{ path: '', alias: '' }])
    setBlockSize(task?.blockSize ?? 1_000_000_000)
    setRetention(task?.retention ?? 3)
    setSchedule(task?.schedule ?? initialSchedule)
    setEndpoint(task?.remote.endpoint ?? 'http://')
    setRemoteRoot(task?.remote.root ?? 'backup')
    setUsername(task?.remote.username ?? '')
    setRemotePassword('')
    setPassword('')
    setRunNow(false)
    setError('')
  }, [open, task])

  const updateSource = (index: number, patch: Partial<SourceRoot>) =>
    setSources((current) =>
      current.map((source, itemIndex) => (itemIndex === index ? { ...source, ...patch } : source)),
    )

  const save = async () => {
    setSaving(true)
    setError('')
    try {
      if (task) {
        const saved = await api<Task>(`/api/tasks/${task.id}`, {
          method: 'PUT',
          ...body({ name, sources, retention, schedule }),
        })
        onSaved(saved, false)
      } else {
        const saved = await api<Task>('/api/tasks', {
          method: 'POST',
          ...body({
            name,
            mode,
            password,
            sources,
            remote: { endpoint, root: remoteRoot, username, password: remotePassword },
            blockSize,
            retention,
            schedule,
          }),
        })
        onSaved(saved, runNow)
      }
      onClose()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onClose={saving ? undefined : onClose} fullWidth maxWidth="md">
      <DialogTitle>{task ? '编辑任务' : '创建备份任务'}</DialogTitle>
      <DialogContent>
        <Stack spacing={3} sx={{ mt: 1 }}>
          {error && <Alert severity="error">{error}</Alert>}
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
            <TextField
              fullWidth
              required
              label="任务名称"
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
            <FormControl fullWidth disabled={Boolean(task)}>
              <InputLabel>任务模式</InputLabel>
              <Select
                label="任务模式"
                value={mode}
                onChange={(event) => setMode(event.target.value as TaskMode)}
              >
                <MenuItem value="snapshot">版本快照</MenuItem>
                <MenuItem value="archive">仅上传归档</MenuItem>
              </Select>
            </FormControl>
          </Stack>
          {!task && (
            <TextField
              required
              fullWidth
              label="任务密码"
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              helperText="不限制强度；忘记后无法恢复。密码只明文保存在NAS本地。"
            />
          )}
          <Box>
            <Typography variant="subtitle1" sx={{ fontWeight: 700 }} gutterBottom>
              源目录
            </Typography>
            <Stack spacing={2}>
              {sources.map((source, index) => (
                <Stack
                  key={index}
                  direction={{ xs: 'column', md: 'row' }}
                  spacing={1}
                  sx={{ alignItems: 'flex-start' }}
                >
                  <Box sx={{ flex: 1, width: '100%' }}>
                    <DirectoryPicker
                      label={`源目录 ${index + 1}`}
                      value={source.path}
                      onChange={(path) => updateSource(index, { path })}
                    />
                  </Box>
                  <TextField
                    label="恢复根名称"
                    value={source.alias}
                    onChange={(event) => updateSource(index, { alias: event.target.value })}
                    helperText="留空自动命名"
                    sx={{ minWidth: 180 }}
                  />
                  <IconButton
                    aria-label="删除源目录"
                    disabled={sources.length === 1}
                    onClick={() =>
                      setSources((current) => current.filter((_, itemIndex) => itemIndex !== index))
                    }
                  >
                    <DeleteOutlineIcon />
                  </IconButton>
                </Stack>
              ))}
              <Button
                startIcon={<AddIcon />}
                onClick={() => setSources((current) => [...current, { path: '', alias: '' }])}
                sx={{ alignSelf: 'flex-start' }}
              >
                添加源目录
              </Button>
            </Stack>
          </Box>
          {!task && (
            <Box>
              <Typography variant="subtitle1" sx={{ fontWeight: 700 }} gutterBottom>
                WebDAV目标
              </Typography>
              <Stack spacing={2}>
                <TextField
                  required
                  label="WebDAV地址"
                  value={endpoint}
                  onChange={(event) => setEndpoint(event.target.value)}
                  placeholder="http://nas:5005/webdav"
                />
                <TextField
                  label="备份根目录"
                  value={remoteRoot}
                  onChange={(event) => setRemoteRoot(event.target.value)}
                />
                <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                  <TextField
                    fullWidth
                    label="用户名"
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                  />
                  <TextField
                    fullWidth
                    label="WebDAV密码"
                    type="password"
                    value={remotePassword}
                    onChange={(event) => setRemotePassword(event.target.value)}
                  />
                </Stack>
              </Stack>
            </Box>
          )}
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
            <FormControl fullWidth disabled={Boolean(task)}>
              <InputLabel>最大远端块</InputLabel>
              <Select
                label="最大远端块"
                value={blockSize}
                onChange={(event) => setBlockSize(Number(event.target.value))}
              >
                <MenuItem value={1_000_000_000}>1 GB</MenuItem>
                <MenuItem value={2_000_000_000}>2 GB</MenuItem>
                <MenuItem value={3_700_000_000}>3.7 GB</MenuItem>
              </Select>
            </FormControl>
            <TextField
              fullWidth
              label="保留最近快照"
              type="number"
              value={retention}
              onChange={(event) => setRetention(Math.max(1, Number(event.target.value)))}
              disabled={mode === 'archive'}
            />
          </Stack>
          <ScheduleFields value={schedule} onChange={setSchedule} />
          {!task && schedule.type !== 'manual' && (
            <FormControlLabel
              control={
                <Checkbox checked={runNow} onChange={(event) => setRunNow(event.target.checked)} />
              }
              label="创建后立即执行首次备份"
            />
          )}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>取消</Button>
        <Button variant="contained" disabled={saving} onClick={() => void save()}>
          {saving ? '正在验证并保存…' : '保存'}
        </Button>
      </DialogActions>
    </Dialog>
  )
}
