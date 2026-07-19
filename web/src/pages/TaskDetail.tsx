import ArchiveIcon from '@mui/icons-material/Archive'
import DeleteForeverIcon from '@mui/icons-material/DeleteForever'
import DownloadIcon from '@mui/icons-material/Download'
import EditIcon from '@mui/icons-material/Edit'
import LockIcon from '@mui/icons-material/Lock'
import PauseIcon from '@mui/icons-material/Pause'
import PlayArrowIcon from '@mui/icons-material/PlayArrow'
import RefreshIcon from '@mui/icons-material/Refresh'
import RestoreIcon from '@mui/icons-material/Restore'
import VerifiedIcon from '@mui/icons-material/Verified'
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  FormControl,
  InputLabel,
  LinearProgress,
  MenuItem,
  Select,
  Stack,
  Tab,
  Tabs,
  TextField,
  Typography,
} from '@mui/material'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, body, formatBytes, formatDate } from '../api'
import { DirectoryPicker } from '../components/DirectoryPicker'
import { FileExplorer } from '../components/FileExplorer'
import { TaskDialog } from '../components/TaskDialog'
import type {
  CheckResult,
  FileEntry,
  RunRecord,
  Snapshot,
  Task,
  TaskProgress,
  WebDAVConfig,
} from '../types'

interface Props {
  taskId: string
  notify: (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void
  onDeleted: () => void
}

type TabName = 'overview' | 'files' | 'snapshots' | 'runs' | 'restore' | 'connection'

export function TaskDetail({ taskId, notify, onDeleted }: Props) {
  const [task, setTask] = useState<Task | null>(null)
  const [snapshots, setSnapshots] = useState<Snapshot[]>([])
  const [files, setFiles] = useState<FileEntry[]>([])
  const [runs, setRuns] = useState<RunRecord[]>([])
  const [progress, setProgress] = useState<TaskProgress | null>(null)
  const [snapshotId, setSnapshotId] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [tab, setTab] = useState<TabName>('overview')
  const [loading, setLoading] = useState(true)
  const [editOpen, setEditOpen] = useState(false)
  const [restoreOutput, setRestoreOutput] = useState('/restore')
  const [importedTaskDirectory, setImportedTaskDirectory] = useState('')
  const [check, setCheck] = useState<CheckResult | null>(null)
  const [action, setAction] = useState<ActionDialog | null>(null)
  const [remote, setRemote] = useState<WebDAVConfig>({
    endpoint: '',
    root: '',
    username: '',
    password: '',
  })
  const [confirmReconnect, setConfirmReconnect] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [loadedTask, loadedSnapshots, loadedRuns, loadedProgress] = await Promise.all([
        api<Task>(`/api/tasks/${taskId}`),
        api<Snapshot[]>(`/api/tasks/${taskId}/snapshots`),
        api<RunRecord[]>(`/api/tasks/${taskId}/runs`),
        api<TaskProgress>(`/api/tasks/${taskId}/progress`),
      ])
      setTask(loadedTask)
      setSnapshots(loadedSnapshots)
      setRuns(loadedRuns)
      setProgress(loadedProgress)
      setRemote(loadedTask.remote)
      const selectedSnapshot =
        loadedTask.mode === 'archive' ? 'archive' : snapshotId || loadedSnapshots[0]?.id || ''
      setSnapshotId(selectedSnapshot)
      if (selectedSnapshot)
        setFiles(
          await api<FileEntry[]>(
            `/api/tasks/${taskId}/files?snapshot=${encodeURIComponent(selectedSnapshot)}`,
          ),
        )
      else setFiles([])
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '任务读取失败', 'error')
    } finally {
      setLoading(false)
    }
  }, [notify, snapshotId, taskId])

  useEffect(() => {
    void load()
  }, [load])

  const taskStatus = task?.status

  useEffect(() => {
    if (!taskStatus || !['queued', 'running', 'paused'].includes(taskStatus)) return
    const timer = window.setInterval(() => {
      void Promise.all([
        api<Task>(`/api/tasks/${taskId}`),
        api<TaskProgress>(`/api/tasks/${taskId}/progress`),
      ])
        .then(([updatedTask, updatedProgress]) => {
          setTask(updatedTask)
          setProgress(updatedProgress)
          if (!['queued', 'running', 'paused'].includes(updatedTask.status)) void load()
        })
        .catch(() => undefined)
    }, 1500)
    return () => window.clearInterval(timer)
  }, [load, taskId, taskStatus])

  const changeSnapshot = async (id: string) => {
    setSnapshotId(id)
    setSelected([])
    try {
      setFiles(
        await api<FileEntry[]>(`/api/tasks/${taskId}/files?snapshot=${encodeURIComponent(id)}`),
      )
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '文件索引读取失败', 'error')
    }
  }

  const perform = async (endpoint: string, success: string) => {
    try {
      await api(endpoint, { method: 'POST', ...body({}) })
      notify(success)
      await load()
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '操作失败', 'error')
    }
  }

  const selectedFiles = useMemo(
    () =>
      selected.length
        ? files.filter((file) => selectedPath(selected, `${file.rootAlias}/${file.relativePath}`))
        : [],
    [files, selected],
  )

  const restore = async () => {
    try {
      const report = await api<{ results: Array<{ status: string }> }>(
        `/api/tasks/${taskId}/restore`,
        { method: 'POST', ...body({ snapshotId, selected, output: restoreOutput }) },
      )
      const failed = report.results.filter(
        (item) => item.status === 'failed' || item.status === 'missing',
      ).length
      notify(
        failed ? `恢复完成，${failed}个文件未恢复` : '恢复完成',
        failed ? 'warning' : 'success',
      )
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '恢复失败', 'error')
    }
  }

  const restoreImported = async () => {
    try {
      const report = await api<{ results: Array<{ status: string }> }>(
        `/api/tasks/${taskId}/restore-imported`,
        {
          method: 'POST',
          ...body({
            snapshotId,
            selected,
            taskDirectory: importedTaskDirectory,
            output: restoreOutput,
          }),
        },
      )
      const failed = report.results.filter(
        (item) => item.status === 'failed' || item.status === 'missing',
      ).length
      notify(
        failed ? `本地块恢复完成，${failed}个文件未恢复` : '本地块恢复完成',
        failed ? 'warning' : 'success',
      )
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '本地块恢复失败', 'error')
    }
  }

  const exportPlan = async () => {
    try {
      const response = await fetch(`/api/tasks/${taskId}/plan`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ snapshotId, selected }),
      })
      if (!response.ok)
        throw new Error(((await response.json()) as { error?: string }).error || '恢复计划生成失败')
      const blob = await response.blob()
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `${task?.name ?? 'task'}-${snapshotId}.backup-plan`
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '导出失败', 'error')
    }
  }

  const quickCheck = async () => {
    try {
      const result = await api<CheckResult>(`/api/tasks/${taskId}/check`, {
        method: 'POST',
        ...body({ snapshotId }),
      })
      setCheck(result)
      notify(
        result.issues.length
          ? `检查发现${result.issues.length}个问题`
          : `已确认${result.checked}个远端对象均存在且大小正确`,
        result.issues.length ? 'warning' : 'success',
      )
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '检查失败', 'error')
    }
  }

  if (!task) return <Box>{loading && <LinearProgress />}</Box>

  return (
    <Stack spacing={3}>
      {loading && <LinearProgress />}
      <Stack
        direction={{ xs: 'column', md: 'row' }}
        sx={{ justifyContent: 'space-between', gap: 2 }}
      >
        <Box>
          <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
            <Typography variant="h4">{task.name}</Typography>
            <Chip
              label={task.mode === 'snapshot' ? '版本快照' : '仅上传归档'}
              color={task.mode === 'snapshot' ? 'primary' : 'secondary'}
              size="small"
            />
            <StatusChip status={task.status} />
          </Stack>
          <Typography color="text.secondary">
            {task.sources.map((source) => source.alias).join('、')} · 最大块{' '}
            {formatBytes(task.blockSize)}
          </Typography>
        </Box>
        <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }}>
          <Button startIcon={<RefreshIcon />} onClick={() => void load()}>
            刷新
          </Button>
          <Button startIcon={<EditIcon />} onClick={() => setEditOpen(true)}>
            编辑
          </Button>
          {task.status === 'running' ? (
            <Button
              startIcon={<PauseIcon />}
              onClick={() => void perform(`/api/tasks/${taskId}/pause`, '将在当前块完成后暂停')}
            >
              暂停
            </Button>
          ) : task.status === 'paused' ? (
            <Button
              startIcon={<PlayArrowIcon />}
              onClick={() => void perform(`/api/tasks/${taskId}/resume`, '任务继续运行')}
            >
              继续
            </Button>
          ) : (
            <Button
              variant="contained"
              startIcon={<PlayArrowIcon />}
              onClick={() => void perform(`/api/tasks/${taskId}/run`, '任务已加入队列')}
            >
              立即执行
            </Button>
          )}
        </Stack>
      </Stack>
      {!task.attachedWritable && (
        <Alert severity="warning">
          任务处于只读状态。完成远端UUID、对象和源目录确认后才能继续写入。
        </Alert>
      )}
      <Tabs
        value={tab}
        onChange={(_, value: TabName) => setTab(value)}
        variant="scrollable"
        allowScrollButtonsMobile
      >
        <Tab value="overview" label="概览" />
        <Tab value="files" label="文件" />
        <Tab value="snapshots" label="版本" />
        <Tab value="runs" label="运行日志" />
        <Tab value="restore" label="恢复" />
        <Tab value="connection" label="远端连接" />
      </Tabs>
      {tab === 'overview' && (
        <Overview
          task={task}
          progress={progress}
          snapshots={snapshots}
          runs={runs}
          check={check}
          onCheck={() => void quickCheck()}
          onCleanup={() =>
            setAction({
              type: 'cleanup',
              title: '删除所有未引用对象',
              description: '只删除当前所有保留版本和归档均未引用的数据对象。操作立即执行。',
            })
          }
        />
      )}
      {tab === 'files' && (
        <Stack spacing={2}>
          <SnapshotSelect
            task={task}
            snapshots={snapshots}
            value={snapshotId}
            onChange={(value) => void changeSnapshot(value)}
          />
          <FileExplorer files={files} selected={selected} onSelected={setSelected} />
          {task.mode === 'archive' && (
            <Button
              color="error"
              startIcon={<DeleteForeverIcon />}
              disabled={!selectedFiles.length}
              onClick={() =>
                setAction({
                  type: 'archive-delete',
                  title: `删除选中的${selectedFiles.length}个归档文件`,
                  description:
                    '小文件包仍被其他文件引用时不会释放整个包。源文件仍存在则下次扫描会重新加入。',
                })
              }
            >
              删除归档文件
            </Button>
          )}
        </Stack>
      )}
      {tab === 'snapshots' && (
        <SnapshotList task={task} snapshots={snapshots} onAction={setAction} />
      )}
      {tab === 'runs' && <RunList runs={runs} />}
      {tab === 'restore' && (
        <Stack spacing={2}>
          <SnapshotSelect
            task={task}
            snapshots={snapshots}
            value={snapshotId}
            onChange={(value) => void changeSnapshot(value)}
          />
          <FileExplorer files={files} selected={selected} onSelected={setSelected} />
          <DirectoryPicker
            label="恢复到NAS目录"
            value={restoreOutput}
            onChange={setRestoreOutput}
          />
          <Stack direction="row" spacing={2}>
            <Button variant="contained" startIcon={<RestoreIcon />} onClick={() => void restore()}>
              恢复所选文件
            </Button>
            <Button startIcon={<DownloadIcon />} onClick={() => void exportPlan()}>
              导出所需块计划
            </Button>
          </Stack>
          <Divider />
          <Typography variant="h6">从已下载的数据块恢复</Typography>
          <Typography color="text.secondary">
            选择保留了 objects
            子目录结构的完整任务目录。可只放回下载计划所列的数据块；缺少的文件会明确报告，不影响其他文件恢复。
          </Typography>
          <DirectoryPicker
            label="已下载任务目录"
            value={importedTaskDirectory}
            onChange={setImportedTaskDirectory}
          />
          <Button
            variant="outlined"
            startIcon={<RestoreIcon />}
            disabled={!importedTaskDirectory}
            onClick={() => void restoreImported()}
          >
            从本地数据块恢复所选文件
          </Button>
        </Stack>
      )}
      {tab === 'connection' && (
        <Connection
          task={task}
          remote={remote}
          setRemote={setRemote}
          confirm={confirmReconnect}
          setConfirm={setConfirmReconnect}
          notify={notify}
          onSaved={load}
        />
      )}
      <TaskDialog
        open={editOpen}
        onClose={() => setEditOpen(false)}
        task={task}
        onSaved={(saved) => {
          setTask(saved)
          notify('任务配置已更新')
        }}
      />
      <ActionPasswordDialog
        action={action}
        onClose={() => setAction(null)}
        onConfirm={async (password, confirmName, note) => {
          if (!action) return
          try {
            if (action.type === 'delete-task') {
              await api(`/api/tasks/${taskId}`, {
                method: 'DELETE',
                ...body({ password, confirmName }),
              })
              onDeleted()
              return
            }
            if (action.type === 'archive-delete')
              await api(`/api/tasks/${taskId}/archive-delete`, {
                method: 'POST',
                ...body({ password, fileIds: selectedFiles.map((file) => file.id) }),
              })
            if (action.type === 'cleanup')
              await api(`/api/tasks/${taskId}/cleanup`, {
                method: 'POST',
                ...body({ password }),
              })
            if (action.type === 'delete-snapshot')
              await api(`/api/tasks/${taskId}/snapshots/${action.snapshotId}`, {
                method: 'DELETE',
                ...body({ password }),
              })
            if (action.type === 'lock-snapshot' || action.type === 'unlock-snapshot')
              await api(`/api/tasks/${taskId}/snapshots/${action.snapshotId}/lock`, {
                method: 'POST',
                ...body({ password, note, locked: action.type === 'lock-snapshot' }),
              })
            notify('操作完成')
            setSelected([])
            await load()
          } catch (reason) {
            notify(reason instanceof Error ? reason.message : '操作失败', 'error')
          }
        }}
        taskName={task.name}
      />
      <Divider />
      <Button
        color="error"
        startIcon={<DeleteForeverIcon />}
        sx={{ alignSelf: 'flex-start' }}
        onClick={() =>
          setAction({
            type: 'delete-task',
            title: '永久删除整个任务',
            description: '这会删除远端任务目录、所有索引和数据块，无法撤销。',
          })
        }
      >
        删除整个任务
      </Button>
    </Stack>
  )
}

function Overview({
  task,
  progress,
  snapshots,
  runs,
  check,
  onCheck,
  onCleanup,
}: {
  task: Task
  progress: TaskProgress | null
  snapshots: Snapshot[]
  runs: RunRecord[]
  check: CheckResult | null
  onCheck: () => void
  onCleanup: () => void
}) {
  const latest = snapshots[0]
  const orphanCount = check?.issues.filter((issue) => issue.kind === 'unreferenced').length ?? 0
  return (
    <Stack spacing={2}>
      <ProgressDetails task={task} progress={progress} />
      <Box
        sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'repeat(3, 1fr)' }, gap: 2 }}
      >
        <Metric title="文件" value={String(latest?.files.length ?? 0)} />
        <Metric
          title="版本"
          value={task.mode === 'archive' ? '持续归档' : String(snapshots.length)}
        />
        <Metric title="最近运行" value={formatDate(task.lastRunAt)} />
      </Box>
      <Card>
        <CardContent>
          <Stack
            direction={{ xs: 'column', md: 'row' }}
            sx={{ justifyContent: 'space-between', gap: 2 }}
          >
            <Box>
              <Typography variant="h6">远端快速检查</Typography>
              <Typography color="text.secondary">
                只核对随机对象名和大小，不下载内容。
                {check ? ` 最近检查${check.checked}项，发现${check.issues.length}个问题。` : ''}
              </Typography>
            </Box>
            <Stack direction="row" spacing={1}>
              <Button startIcon={<VerifiedIcon />} variant="outlined" onClick={onCheck}>
                开始检查
              </Button>
              {orphanCount > 0 && (
                <Button color="error" onClick={onCleanup}>
                  清理{orphanCount}个未引用对象
                </Button>
              )}
            </Stack>
          </Stack>
          {check?.issues.map((issue) => (
            <Alert key={`${issue.kind}-${issue.path}`} severity="warning" sx={{ mt: 1 }}>
              {issue.kind}: {issue.path}
            </Alert>
          ))}
        </CardContent>
      </Card>
      <Card>
        <CardContent>
          <Typography variant="h6" gutterBottom>
            最近结果
          </Typography>
          <Typography color="text.secondary">{runs[0]?.message ?? '尚未运行'}</Typography>
        </CardContent>
      </Card>
    </Stack>
  )
}

function ProgressDetails({ task, progress }: { task: Task; progress: TaskProgress | null }) {
  const active = ['queued', 'running', 'paused'].includes(task.status)
  const percent = Math.max(0, Math.min(100, progress?.percent ?? 0))
  const phaseLabels: Record<string, string> = {
    idle: '空闲',
    queued: '排队中',
    scanning: '扫描目录',
    hashing: '比较与计算哈希',
    uploading: '加密并上传',
    finalizing: '发布索引',
    paused: '暂停中',
    running: '运行中',
    completed: '已完成',
    incomplete: '不完整完成',
    failed: '失败',
  }
  return (
    <Card>
      <CardContent>
        <Stack spacing={1.5}>
          <Stack direction="row" sx={{ justifyContent: 'space-between', gap: 2 }}>
            <Box>
              <Typography variant="h6">任务进度</Typography>
              <Typography color="text.secondary">
                {phaseLabels[progress?.phase ?? task.status] ?? progress?.phase ?? task.status} ·{' '}
                {progress?.message ?? '当前没有运行中的备份'}
              </Typography>
            </Box>
            <Typography variant="h5">
              {active || percent > 0 ? `${percent.toFixed(1)}%` : '—'}
            </Typography>
          </Stack>
          <LinearProgress
            variant={progress?.phase === 'scanning' ? 'indeterminate' : 'determinate'}
            value={percent}
            color={
              progress?.phase === 'failed'
                ? 'error'
                : progress?.phase === 'incomplete'
                  ? 'warning'
                  : 'primary'
            }
          />
          <Box
            sx={{
              display: 'grid',
              gridTemplateColumns: { xs: '1fr 1fr', md: 'repeat(4, 1fr)' },
              gap: 1,
            }}
          >
            <Typography variant="body2">
              文件：{progress?.filesProcessed ?? 0}/{progress?.filesTotal ?? 0}
            </Typography>
            <Typography variant="body2">
              对象：{progress?.objectsCompleted ?? 0}/{progress?.objectsTotal ?? 0}
            </Typography>
            <Typography variant="body2">
              数据：{formatBytes(progress?.bytesCompleted ?? 0)}/
              {formatBytes(progress?.bytesTotal ?? 0)}
            </Typography>
            <Typography variant="body2">更新：{formatDate(progress?.updatedAt)}</Typography>
          </Box>
          {progress?.currentFile && (
            <Typography
              variant="body2"
              sx={{ fontFamily: 'monospace', overflowWrap: 'anywhere', color: 'text.secondary' }}
            >
              当前：{progress.currentFile}
            </Typography>
          )}
        </Stack>
      </CardContent>
    </Card>
  )
}

function Metric({ title, value }: { title: string; value: string }) {
  return (
    <Card>
      <CardContent>
        <Typography color="text.secondary" variant="body2">
          {title}
        </Typography>
        <Typography variant="h5" sx={{ mt: 1 }}>
          {value}
        </Typography>
      </CardContent>
    </Card>
  )
}

function SnapshotSelect({
  task,
  snapshots,
  value,
  onChange,
}: {
  task: Task
  snapshots: Snapshot[]
  value: string
  onChange: (value: string) => void
}) {
  if (task.mode === 'archive')
    return (
      <Alert icon={<ArchiveIcon />} severity="info">
        归档模式只有当前归档，路径采用内容首次发现时的位置。
      </Alert>
    )
  return (
    <FormControl sx={{ minWidth: 320 }}>
      <InputLabel>版本</InputLabel>
      <Select label="版本" value={value} onChange={(event) => onChange(event.target.value)}>
        {snapshots.map((snapshot) => (
          <MenuItem key={snapshot.id} value={snapshot.id}>
            {formatDate(snapshot.createdAt)} · {snapshot.complete ? '完整' : '不完整'}
            {snapshot.locked ? ' · 永久' : ''}
          </MenuItem>
        ))}
      </Select>
    </FormControl>
  )
}

function SnapshotList({
  task,
  snapshots,
  onAction,
}: {
  task: Task
  snapshots: Snapshot[]
  onAction: (action: ActionDialog) => void
}) {
  if (task.mode === 'archive') return <Alert severity="info">归档模式不积累版本。</Alert>
  return (
    <Stack spacing={1}>
      {snapshots.map((snapshot) => (
        <Card key={snapshot.id}>
          <CardContent>
            <Stack
              direction={{ xs: 'column', sm: 'row' }}
              sx={{ justifyContent: 'space-between', gap: 2 }}
            >
              <Box>
                <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                  <Typography sx={{ fontWeight: 700 }}>{formatDate(snapshot.createdAt)}</Typography>
                  <Chip
                    size="small"
                    color={snapshot.complete ? 'success' : 'warning'}
                    label={snapshot.complete ? '完整' : '不完整'}
                  />
                  {snapshot.locked && <Chip size="small" icon={<LockIcon />} label="永久保留" />}
                </Stack>
                <Typography variant="body2" color="text.secondary">
                  {snapshot.files.length}个文件 · {snapshot.changeCount}项变化
                  {snapshot.lockNote ? ` · ${snapshot.lockNote}` : ''}
                </Typography>
              </Box>
              <Stack direction="row" spacing={1}>
                <Button
                  onClick={() =>
                    onAction({
                      type: snapshot.locked ? 'unlock-snapshot' : 'lock-snapshot',
                      snapshotId: snapshot.id,
                      title: snapshot.locked ? '解除永久保留' : '永久保留此版本',
                      description: snapshot.complete
                        ? ''
                        : '这是不完整版本，永久保留前请确认缺失文件。',
                    })
                  }
                >
                  {snapshot.locked ? '解锁' : '永久保留'}
                </Button>
                <Button
                  color="error"
                  disabled={snapshot.locked}
                  onClick={() =>
                    onAction({
                      type: 'delete-snapshot',
                      snapshotId: snapshot.id,
                      title: '删除整个版本',
                      description: '无人引用的数据块会立即删除。',
                    })
                  }
                >
                  删除
                </Button>
              </Stack>
            </Stack>
          </CardContent>
        </Card>
      ))}
    </Stack>
  )
}

function RunList({ runs }: { runs: RunRecord[] }) {
  return (
    <Stack spacing={1}>
      {runs.length ? (
        runs.map((run) => (
          <Card key={run.id}>
            <CardContent>
              <Stack
                direction={{ xs: 'column', sm: 'row' }}
                sx={{ justifyContent: 'space-between' }}
              >
                <Box>
                  <Typography sx={{ fontWeight: 700 }}>{formatDate(run.startedAt)}</Typography>
                  <Typography color="text.secondary">{run.message || run.status}</Typography>
                </Box>
                <Box sx={{ textAlign: { sm: 'right' } }}>
                  <Chip
                    size="small"
                    label={run.status}
                    color={
                      run.status === 'complete'
                        ? 'success'
                        : run.status === 'incomplete'
                          ? 'warning'
                          : run.status === 'failed'
                            ? 'error'
                            : 'default'
                    }
                  />
                  <Typography variant="body2" color="text.secondary">
                    扫描{run.filesScanned} · 新增{run.filesAdded} · {formatBytes(run.bytesUploaded)}
                  </Typography>
                </Box>
              </Stack>
              {run.details?.map((detail) => (
                <Alert severity="warning" key={detail} sx={{ mt: 1 }}>
                  {detail}
                </Alert>
              ))}
            </CardContent>
          </Card>
        ))
      ) : (
        <Alert severity="info">尚无运行记录</Alert>
      )}
    </Stack>
  )
}

function Connection({
  task,
  remote,
  setRemote,
  confirm,
  setConfirm,
  notify,
  onSaved,
}: {
  task: Task
  remote: WebDAVConfig
  setRemote: (value: WebDAVConfig) => void
  confirm: boolean
  setConfirm: (value: boolean) => void
  notify: Props['notify']
  onSaved: () => Promise<void>
}) {
  const save = async () => {
    try {
      const result = await api<{ writable: boolean; check: CheckResult }>(
        `/api/tasks/${task.id}/reconnect`,
        { method: 'POST', ...body({ remote, confirmWrite: confirm }) },
      )
      notify(
        result.writable
          ? '远端UUID和对象检查通过，已恢复写入'
          : `只读检查完成，发现${result.check.issues.length}个问题`,
        result.writable ? 'success' : 'warning',
      )
      await onSaved()
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '连接失败', 'error')
    }
  }
  return (
    <Stack spacing={2}>
      <Alert severity="info">
        修改IP、认证或根路径后先只读发现永久任务UUID；首次检查通过后再次勾选确认写入。
      </Alert>
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
          value={remote.username ?? ''}
          onChange={(event) => setRemote({ ...remote, username: event.target.value })}
        />
        <TextField
          fullWidth
          type="password"
          label="WebDAV密码（留空表示空密码）"
          value={remote.password ?? ''}
          onChange={(event) => setRemote({ ...remote, password: event.target.value })}
        />
      </Stack>
      <Button variant="contained" onClick={() => void save()}>
        {confirm ? '确认匹配并允许写入' : '只读重新发现'}
      </Button>
      <Button onClick={() => setConfirm(!confirm)}>
        {confirm ? '取消写入确认' : '下一次操作确认允许写入'}
      </Button>
    </Stack>
  )
}

interface ActionDialog {
  type:
    | 'delete-task'
    | 'archive-delete'
    | 'delete-snapshot'
    | 'lock-snapshot'
    | 'unlock-snapshot'
    | 'cleanup'
  title: string
  description: string
  snapshotId?: string
}

function ActionPasswordDialog({
  action,
  onClose,
  onConfirm,
  taskName,
}: {
  action: ActionDialog | null
  onClose: () => void
  onConfirm: (password: string, confirmName: string, note: string) => Promise<void>
  taskName: string
}) {
  const [password, setPassword] = useState('')
  const [name, setName] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => {
    if (action) {
      setPassword('')
      setName('')
      setNote('')
    }
  }, [action])
  return (
    <Dialog open={Boolean(action)} onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>{action?.title}</DialogTitle>
      <DialogContent>
        <Stack spacing={2} sx={{ mt: 1 }}>
          {action?.description && <Alert severity="warning">{action.description}</Alert>}
          <TextField
            label="任务密码"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
          {action?.type === 'delete-task' && (
            <TextField
              label={`输入任务名“${taskName}”`}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          )}
          {action?.type === 'lock-snapshot' && (
            <TextField
              label="备注"
              value={note}
              onChange={(event) => setNote(event.target.value)}
            />
          )}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>取消</Button>
        <Button
          color={action?.type.includes('delete') ? 'error' : 'primary'}
          variant="contained"
          disabled={!password || (action?.type === 'delete-task' && name !== taskName)}
          onClick={() => {
            void onConfirm(password, name, note)
            onClose()
          }}
        >
          确认
        </Button>
      </DialogActions>
    </Dialog>
  )
}

function StatusChip({ status }: { status: Task['status'] }) {
  const labels: Record<Task['status'], string> = {
    idle: '空闲',
    queued: '排队',
    running: '运行中',
    paused: '已暂停',
    failed: '失败',
    read_only: '只读',
    needs_input: '等待处理',
  }
  const color =
    status === 'running'
      ? 'primary'
      : status === 'failed'
        ? 'error'
        : status === 'paused' || status === 'read_only'
          ? 'warning'
          : 'default'
  return <Chip size="small" label={labels[status]} color={color} />
}

function selectedPath(selected: string[], path: string) {
  return (
    selected.length === 0 || selected.some((item) => path === item || path.startsWith(`${item}/`))
  )
}
