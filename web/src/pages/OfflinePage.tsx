import FolderOpenIcon from '@mui/icons-material/FolderOpen'
import RestoreIcon from '@mui/icons-material/Restore'
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  FormControl,
  InputLabel,
  LinearProgress,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { api, body, formatBytes, formatDate } from '../api'
import { DirectoryPicker } from '../components/DirectoryPicker'
import { FileExplorer } from '../components/FileExplorer'
import type { FileEntry, OfflineOpenResult, RestoreProgress } from '../types'

interface Props {
  notify: (message: string, severity?: 'success' | 'error' | 'warning') => void
}

export function OfflinePage({ notify }: Props) {
  const [taskDirectory, setTaskDirectory] = useState('')
  const [password, setPassword] = useState('')
  const [opened, setOpened] = useState<OfflineOpenResult | null>(null)
  const [files, setFiles] = useState<FileEntry[]>([])
  const [snapshot, setSnapshot] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [output, setOutput] = useState('')
  const [busy, setBusy] = useState(false)
  const [restoring, setRestoring] = useState(false)
  const [restoreProgress, setRestoreProgress] = useState<RestoreProgress | null>(null)

  useEffect(() => {
    if (!restoring) return
    let active = true
    const refresh = async () => {
      try {
        const progress = await api<RestoreProgress>('/api/offline/progress')
        if (active) setRestoreProgress(progress)
      } catch {
        // 恢复请求本身会报告错误；短暂轮询失败不打断恢复。
      }
    }
    void refresh()
    const timer = window.setInterval(() => void refresh(), 750)
    return () => {
      active = false
      window.clearInterval(timer)
    }
  }, [restoring])

  const open = async () => {
    setBusy(true)
    try {
      const result = await api<OfflineOpenResult>('/api/offline/open', {
        method: 'POST',
        ...body({ directory: taskDirectory, password }),
      })
      setOpened(result)
      setSnapshot(result.selected)
      setFiles(await api<FileEntry[]>('/api/offline/files'))
      setSelected([])
      notify(
        result.salvaged ? '索引不可用，已通过数据块自恢复头重建可抢救文件' : '完整任务索引读取成功',
        result.salvaged ? 'warning' : 'success',
      )
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '任务打开失败', 'error')
    } finally {
      setBusy(false)
    }
  }

  const chooseSnapshot = async (id: string) => {
    setBusy(true)
    try {
      await api('/api/offline/select', { method: 'POST', ...body({ snapshotId: id }) })
      setSnapshot(id)
      setFiles(await api<FileEntry[]>('/api/offline/files'))
      setSelected([])
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '版本读取失败', 'error')
    } finally {
      setBusy(false)
    }
  }

  const restore = async () => {
    setBusy(true)
    setRestoring(true)
    setRestoreProgress(null)
    try {
      const report = await api<{ results: Array<{ status: string; verified: boolean }> }>(
        '/api/offline/restore',
        {
          method: 'POST',
          ...body({ selected, output }),
        },
      )
      const failures = report.results.filter((result) =>
        ['failed', 'missing', 'invalid'].includes(result.status),
      ).length
      const verified = report.results.filter((result) => result.verified).length
      notify(
        failures
          ? `恢复完成，已哈希核验${verified}个文件，${failures}个文件未恢复；报告已写入目标目录`
          : `恢复完成，${verified}个文件通过SHA-256核验；HTML和JSON报告已写入目标目录`,
        failures ? 'warning' : 'success',
      )
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '恢复失败', 'error')
    } finally {
      try {
        setRestoreProgress(await api<RestoreProgress>('/api/offline/progress'))
      } catch {
        // 保留最后一次成功轮询的进度。
      }
      setRestoring(false)
      setBusy(false)
    }
  }

  return (
    <Box
      sx={{
        minHeight: '100vh',
        background: 'linear-gradient(135deg,#ecfeff 0%,#f8fafc 48%,#eff6ff 100%)',
        py: { xs: 3, md: 7 },
      }}
    >
      <Box sx={{ maxWidth: 1100, mx: 'auto', px: 2 }}>
        <Stack spacing={3}>
          <Box>
            <Typography variant="overline" color="primary" sx={{ fontWeight: 800 }}>
              WINDOWS 便携恢复端
            </Typography>
            <Typography variant="h3" sx={{ fontWeight: 800 }}>
              WebDAV 冷备份恢复
            </Typography>
            <Typography color="text.secondary" sx={{ mt: 1 }}>
              完全离线运行，不连接WebDAV。选择已经完整下载的任务目录，输入任务密码后恢复全部或部分文件。
            </Typography>
          </Box>
          {busy && !restoring && <LinearProgress />}
          {restoreProgress && restoreProgress.status !== 'idle' && (
            <RestoreProgressCard progress={restoreProgress} />
          )}
          <Card>
            <CardContent>
              <Stack spacing={2}>
                <Typography variant="h6">1. 打开完整任务</Typography>
                <DirectoryPicker
                  label="任务目录"
                  value={taskDirectory}
                  onChange={setTaskDirectory}
                  helperText="目录中应包含task-a.json、catalog索引和objects目录"
                />
                <TextField
                  label="任务密码"
                  type="password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                />
                <Button
                  variant="contained"
                  startIcon={<FolderOpenIcon />}
                  disabled={!taskDirectory || !password || busy}
                  onClick={() => void open()}
                  sx={{ alignSelf: 'flex-start' }}
                >
                  打开任务
                </Button>
              </Stack>
            </CardContent>
          </Card>
          {opened && (
            <>
              <Card>
                <CardContent>
                  <Stack spacing={2}>
                    <Stack
                      direction={{ xs: 'column', sm: 'row' }}
                      sx={{ justifyContent: 'space-between' }}
                    >
                      <Box>
                        <Typography variant="h5">{opened.task.name}</Typography>
                        <Typography color="text.secondary">
                          {opened.task.mode === 'snapshot' ? '版本快照' : '持续归档'} · 最大块{' '}
                          {formatBytes(opened.task.blockSize)}
                        </Typography>
                      </Box>
                      {opened.salvaged && <Chip color="warning" label="自恢复模式" />}
                    </Stack>
                    {opened.salvaged && (
                      <Alert severity="warning">
                        只能按块内首次路径恢复，不能证明是否存在整块删除，也无法还原后续移动后的目录。
                      </Alert>
                    )}
                    <FormControl>
                      <InputLabel>恢复版本</InputLabel>
                      <Select
                        label="恢复版本"
                        value={snapshot}
                        onChange={(event) => void chooseSnapshot(event.target.value)}
                      >
                        {opened.snapshots.map((item) => (
                          <MenuItem key={item.id} value={item.id}>
                            {formatDate(item.createdAt)} · {item.complete ? '完整' : '不完整'} ·{' '}
                            {item.fileCount}个文件
                          </MenuItem>
                        ))}
                      </Select>
                    </FormControl>
                  </Stack>
                </CardContent>
              </Card>
              <Card>
                <CardContent>
                  <Stack spacing={2}>
                    <Typography variant="h6">2. 选择文件</Typography>
                    <FileExplorer files={files} selected={selected} onSelected={setSelected} />
                    <Typography variant="body2" color="text.secondary">
                      未勾选任何项时恢复全部可用文件；不完整文件会预先排除。
                    </Typography>
                  </Stack>
                </CardContent>
              </Card>
              <Card>
                <CardContent>
                  <Stack spacing={2}>
                    <Typography variant="h6">3. 选择恢复目录</Typography>
                    <DirectoryPicker label="恢复目标目录" value={output} onChange={setOutput} />
                    <Alert severity="info">
                      同名且内容一致的文件跳过；同名但内容不同的文件进入“冲突文件”目录；Windows非法文件名会跳过并写入报告。
                    </Alert>
                    <Button
                      variant="contained"
                      size="large"
                      startIcon={<RestoreIcon />}
                      disabled={!output || busy}
                      onClick={() => void restore()}
                      sx={{ alignSelf: 'flex-start' }}
                    >
                      开始恢复
                    </Button>
                  </Stack>
                </CardContent>
              </Card>
            </>
          )}
        </Stack>
      </Box>
    </Box>
  )
}

function RestoreProgressCard({ progress }: { progress: RestoreProgress }) {
  const phaseNames: Record<string, string> = {
    queued: '准备恢复',
    preflight: '预检对象',
    preparing: '准备路径',
    restoring: '解密写入',
    verifying: '哈希核验',
    completed: '恢复完成',
    failed: '恢复失败',
  }
  const color =
    progress.status === 'failed' ? 'error' : progress.status === 'completed' ? 'success' : 'primary'
  return (
    <Card>
      <CardContent>
        <Stack spacing={2}>
          <Stack direction="row" sx={{ justifyContent: 'space-between', gap: 2 }}>
            <Box>
              <Typography variant="h6">
                恢复进度 · {phaseNames[progress.phase] ?? progress.phase}
              </Typography>
              <Typography color="text.secondary">{progress.error || progress.message}</Typography>
            </Box>
            <Typography variant="h5" sx={{ fontWeight: 800 }}>
              {Math.max(0, Math.min(100, progress.percent)).toFixed(1)}%
            </Typography>
          </Stack>
          <LinearProgress
            variant="determinate"
            value={Math.max(0, Math.min(100, progress.percent))}
            color={color}
            sx={{ height: 10, borderRadius: 5 }}
          />
          <Box
            sx={{
              display: 'grid',
              gridTemplateColumns: { xs: '1fr 1fr', md: 'repeat(4,1fr)' },
              gap: 1.5,
            }}
          >
            <ProgressMetric
              label="文件"
              value={`${progress.filesCompleted}/${progress.filesTotal}`}
            />
            <ProgressMetric
              label="对象核对"
              value={`${progress.objectsChecked}/${progress.objectsCheckTotal}`}
            />
            <ProgressMetric
              label="对象读取"
              value={`${progress.objectsCompleted}/${progress.objectsTotal}`}
            />
            <ProgressMetric
              label="恢复数据"
              value={`${formatBytes(progress.bytesCompleted)}/${formatBytes(progress.bytesTotal)}`}
            />
            <ProgressMetric
              label="哈希数据"
              value={`${formatBytes(progress.verifyBytesCompleted)}/${formatBytes(progress.verifyBytesTotal)}`}
            />
            <ProgressMetric label="核验通过" value={`${progress.verifiedFiles}`} />
            <ProgressMetric label="已恢复" value={`${progress.restoredFiles}`} />
            <ProgressMetric
              label="跳过 / 失败"
              value={`${progress.skippedFiles} / ${progress.failedFiles}`}
            />
          </Box>
          {progress.currentObject && (
            <Typography variant="body2" sx={{ fontFamily: 'monospace', overflowWrap: 'anywhere' }}>
              当前对象：{progress.currentObject}
            </Typography>
          )}
          {progress.currentFile && (
            <Typography variant="body2" sx={{ fontFamily: 'monospace', overflowWrap: 'anywhere' }}>
              当前文件：{progress.currentFile}
            </Typography>
          )}
        </Stack>
      </CardContent>
    </Card>
  )
}

function ProgressMetric({ label, value }: { label: string; value: string }) {
  return (
    <Box sx={{ p: 1.25, borderRadius: 1, bgcolor: 'action.hover' }}>
      <Typography variant="caption" color="text.secondary">
        {label}
      </Typography>
      <Typography sx={{ fontWeight: 700 }}>{value}</Typography>
    </Box>
  )
}
