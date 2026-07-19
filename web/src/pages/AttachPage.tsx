import CloudSyncIcon from '@mui/icons-material/CloudSync'
import {
  Alert,
  Button,
  Card,
  CardContent,
  Chip,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import { useState } from 'react'
import { api, body, formatDate } from '../api'
import type { CheckResult, Task, WebDAVConfig } from '../types'

interface Descriptor {
  taskId: string
  name: string
  mode: string
  createdAt: string
}

interface Props {
  onAttached: (task: Task) => void
  notify: (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void
}

export function AttachPage({ onAttached, notify }: Props) {
  const [remote, setRemote] = useState<WebDAVConfig>({
    endpoint: 'http://',
    root: 'backup',
    username: '',
    password: '',
  })
  const [descriptors, setDescriptors] = useState<Descriptor[]>([])
  const [taskName, setTaskName] = useState('')
  const [password, setPassword] = useState('')
  const [result, setResult] = useState<{
    task: Task
    differences: string[]
    check: CheckResult
    writable: boolean
  } | null>(null)
  const [busy, setBusy] = useState(false)

  const discover = async () => {
    setBusy(true)
    try {
      const found = await api<Descriptor[]>('/api/remotes/discover', {
        method: 'POST',
        ...body(remote),
      })
      setDescriptors(found)
      setTaskName(found[0]?.name ?? '')
      notify(`发现${found.length}个任务`)
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '发现失败', 'error')
    } finally {
      setBusy(false)
    }
  }

  const attach = async () => {
    setBusy(true)
    try {
      const attached = await api<{
        task: Task
        differences: string[]
        check: CheckResult
        writable: boolean
      }>('/api/remotes/attach', {
        method: 'POST',
        ...body({ remote, taskName, password, sources: [] }),
      })
      setResult(attached)
      notify(
        attached.writable ? '任务接管完成并允许继续备份' : '任务已恢复为只读，请处理差异',
        attached.writable ? 'success' : 'warning',
      )
      onAttached(attached.task)
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '接管失败', 'error')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Stack spacing={3}>
      <div>
        <Typography variant="h4">从远端恢复任务</Typography>
        <Typography color="text.secondary">
          适用于更换WebDAV地址、凭据或本地数据库完全丢失。首次操作始终只读。
        </Typography>
      </div>
      <Alert severity="info">
        软件读取一级任务目录中的双份描述和加密索引，通过永久UUID识别任务。目录、大小与远端对象全部匹配后才允许继续写入。
      </Alert>
      <Card>
        <CardContent>
          <Stack spacing={2}>
            <Typography variant="h6">1. 连接WebDAV备份根</Typography>
            <TextField
              label="WebDAV地址"
              value={remote.endpoint}
              onChange={(event) => setRemote({ ...remote, endpoint: event.target.value })}
            />
            <TextField
              label="备份根目录"
              value={remote.root}
              onChange={(event) => setRemote({ ...remote, root: event.target.value })}
            />
            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
              <TextField
                fullWidth
                label="用户名"
                value={remote.username}
                onChange={(event) => setRemote({ ...remote, username: event.target.value })}
              />
              <TextField
                fullWidth
                type="password"
                label="WebDAV密码"
                value={remote.password}
                onChange={(event) => setRemote({ ...remote, password: event.target.value })}
              />
            </Stack>
            <Button
              startIcon={<CloudSyncIcon />}
              variant="outlined"
              disabled={busy}
              onClick={() => void discover()}
            >
              只读发现任务
            </Button>
          </Stack>
        </CardContent>
      </Card>
      {descriptors.length > 0 && (
        <Card>
          <CardContent>
            <Stack spacing={2}>
              <Typography variant="h6">2. 选择任务并解密索引</Typography>
              <FormControl>
                <InputLabel>远端任务</InputLabel>
                <Select
                  label="远端任务"
                  value={taskName}
                  onChange={(event) => setTaskName(event.target.value)}
                >
                  {descriptors.map((descriptor) => (
                    <MenuItem key={descriptor.taskId} value={descriptor.name}>
                      {descriptor.name} · {descriptor.mode === 'snapshot' ? '快照' : '归档'} ·{' '}
                      {formatDate(descriptor.createdAt)}
                    </MenuItem>
                  ))}
                </Select>
              </FormControl>
              <TextField
                label="任务密码"
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
              <Button
                variant="contained"
                disabled={busy || !taskName || !password}
                onClick={() => void attach()}
              >
                恢复本地任务
              </Button>
            </Stack>
          </CardContent>
        </Card>
      )}
      {result && (
        <Card>
          <CardContent>
            <Stack spacing={1}>
              <Stack direction="row" spacing={1}>
                <Typography variant="h6">接管结果</Typography>
                <Chip
                  color={result.writable ? 'success' : 'warning'}
                  label={result.writable ? '可写' : '只读'}
                />
              </Stack>
              <Typography>
                快速检查：{result.check.checked}项，{result.check.issues.length}个问题
              </Typography>
              {result.differences.slice(0, 100).map((difference) => (
                <Alert severity="warning" key={difference}>
                  {difference}
                </Alert>
              ))}
              {result.differences.length > 100 && (
                <Typography color="text.secondary">
                  另有{result.differences.length - 100}项差异
                </Typography>
              )}
            </Stack>
          </CardContent>
        </Card>
      )}
    </Stack>
  )
}
