import AddIcon from '@mui/icons-material/Add'
import BackupIcon from '@mui/icons-material/Backup'
import CloudDownloadIcon from '@mui/icons-material/CloudDownload'
import DashboardIcon from '@mui/icons-material/Dashboard'
import MenuIcon from '@mui/icons-material/Menu'
import SettingsIcon from '@mui/icons-material/Settings'
import {
  Alert,
  AppBar,
  Box,
  Button,
  Card,
  CardActionArea,
  CardContent,
  Chip,
  CircularProgress,
  Drawer,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Snackbar,
  Stack,
  Toolbar,
  Typography,
  useMediaQuery,
  useTheme,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { api, body, formatBytes, formatDate } from './api'
import { TaskDialog } from './components/TaskDialog'
import { AttachPage } from './pages/AttachPage'
import { OfflinePage } from './pages/OfflinePage'
import { SettingsPage } from './pages/SettingsPage'
import { TaskDetail } from './pages/TaskDetail'
import type { RuntimeInfo, Task } from './types'

type Page = 'tasks' | 'attach' | 'settings' | 'detail'
type Notice = { message: string; severity: 'success' | 'error' | 'warning' | 'info' }
const drawerWidth = 248

export default function App() {
  const [runtimeInfo, setRuntimeInfo] = useState<RuntimeInfo | null>(null)
  const [runtimeError, setRuntimeError] = useState('')
  const [notice, setNotice] = useState<Notice | null>(null)
  const notify = useCallback(
    (message: string, severity: Notice['severity'] = 'success') => setNotice({ message, severity }),
    [],
  )

  useEffect(() => {
    api<RuntimeInfo>('/api/runtime')
      .then(setRuntimeInfo)
      .catch((reason: Error) => setRuntimeError(reason.message))
  }, [])

  if (runtimeError) return <Alert severity="error">无法连接本地服务：{runtimeError}</Alert>
  if (!runtimeInfo)
    return (
      <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '100vh' }}>
        <CircularProgress />
      </Box>
    )

  return (
    <>
      <Snackbar
        open={Boolean(notice)}
        autoHideDuration={5000}
        onClose={() => setNotice(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'center' }}
      >
        {notice ? (
          <Alert severity={notice.severity} variant="filled" onClose={() => setNotice(null)}>
            {notice.message}
          </Alert>
        ) : undefined}
      </Snackbar>
      {runtimeInfo.mode === 'offline' ? (
        <OfflinePage notify={notify} />
      ) : (
        <ServerApp runtimeInfo={runtimeInfo} notify={notify} />
      )}
    </>
  )
}

function ServerApp({
  runtimeInfo,
  notify,
}: {
  runtimeInfo: RuntimeInfo
  notify: (message: string, severity?: Notice['severity']) => void
}) {
  const [tasks, setTasks] = useState<Task[]>([])
  const [page, setPage] = useState<Page>('tasks')
  const [selectedTask, setSelectedTask] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [mobileOpen, setMobileOpen] = useState(false)
  const theme = useTheme()
  const desktop = useMediaQuery(theme.breakpoints.up('md'))

  const loadTasks = useCallback(async () => {
    try {
      setTasks(await api<Task[]>('/api/tasks'))
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '任务读取失败', 'error')
    }
  }, [notify])
  useEffect(() => {
    void loadTasks()
  }, [loadTasks])

  const navigate = (next: Page) => {
    setPage(next)
    setSelectedTask('')
    setMobileOpen(false)
  }
  const openTask = (id: string) => {
    setSelectedTask(id)
    setPage('detail')
    setMobileOpen(false)
  }
  const drawer = (
    <Box sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <Toolbar>
        <Stack direction="row" spacing={1.5} sx={{ alignItems: 'center' }}>
          <Box
            sx={{
              width: 38,
              height: 38,
              display: 'grid',
              placeItems: 'center',
              borderRadius: 2,
              bgcolor: 'primary.main',
              color: 'primary.contrastText',
            }}
          >
            <BackupIcon />
          </Box>
          <Box>
            <Typography sx={{ fontWeight: 800 }}>冷备份</Typography>
            <Typography variant="caption" color="text.secondary">
              {runtimeInfo.version}
            </Typography>
          </Box>
        </Stack>
      </Toolbar>
      <List sx={{ px: 1.5 }}>
        <NavItem
          icon={<DashboardIcon />}
          label="备份任务"
          selected={page === 'tasks' || page === 'detail'}
          onClick={() => navigate('tasks')}
        />
        <NavItem
          icon={<CloudDownloadIcon />}
          label="从远端恢复任务"
          selected={page === 'attach'}
          onClick={() => navigate('attach')}
        />
        <NavItem
          icon={<SettingsIcon />}
          label="全局设置"
          selected={page === 'settings'}
          onClick={() => navigate('settings')}
        />
      </List>
      <Box sx={{ mt: 'auto', p: 2 }}>
        <Alert severity="info" icon={false}>
          局域网无鉴权
          <br />
          {runtimeInfo.platform}
        </Alert>
      </Box>
    </Box>
  )

  return (
    <Box sx={{ display: 'flex', minHeight: '100vh' }}>
      <AppBar
        position="fixed"
        color="inherit"
        elevation={0}
        sx={{
          borderBottom: 1,
          borderColor: 'divider',
          width: { md: `calc(100% - ${drawerWidth}px)` },
          ml: { md: `${drawerWidth}px` },
        }}
      >
        <Toolbar>
          <IconButton
            aria-label="打开菜单"
            onClick={() => setMobileOpen(true)}
            sx={{ display: { md: 'none' }, mr: 1 }}
          >
            <MenuIcon />
          </IconButton>
          <Typography variant="h6">WebDAV Cold Backup</Typography>
        </Toolbar>
      </AppBar>
      <Box component="nav" sx={{ width: { md: drawerWidth }, flexShrink: { md: 0 } }}>
        <Drawer
          variant={desktop ? 'permanent' : 'temporary'}
          open={desktop || mobileOpen}
          onClose={() => setMobileOpen(false)}
          ModalProps={{ keepMounted: true }}
          sx={{ '& .MuiDrawer-paper': { width: drawerWidth, boxSizing: 'border-box' } }}
        >
          {drawer}
        </Drawer>
      </Box>
      <Box
        component="main"
        sx={{
          flexGrow: 1,
          minWidth: 0,
          p: { xs: 2, md: 4 },
          pt: { xs: 11, md: 12 },
          maxWidth: 1600,
          mx: 'auto',
        }}
      >
        {page === 'tasks' && (
          <TaskList tasks={tasks} onOpen={openTask} onCreate={() => setCreateOpen(true)} />
        )}
        {page === 'detail' && selectedTask && (
          <TaskDetail
            taskId={selectedTask}
            notify={notify}
            onDeleted={() => {
              notify('任务已永久删除')
              void loadTasks()
              navigate('tasks')
            }}
          />
        )}
        {page === 'attach' && (
          <AttachPage
            notify={notify}
            onAttached={(task) => {
              void loadTasks()
              openTask(task.id)
            }}
          />
        )}
        {page === 'settings' && <SettingsPage notify={notify} />}
      </Box>
      <TaskDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onSaved={(task, runNow) => {
          void loadTasks()
          if (runNow)
            api(`/api/tasks/${task.id}/run`, { method: 'POST', ...body({}) })
              .then(() => notify('任务已创建并加入队列'))
              .catch((reason: Error) => notify(reason.message, 'error'))
          else notify('任务已创建')
          openTask(task.id)
        }}
      />
    </Box>
  )
}

function NavItem({
  icon,
  label,
  selected,
  onClick,
}: {
  icon: React.ReactNode
  label: string
  selected: boolean
  onClick: () => void
}) {
  return (
    <ListItemButton selected={selected} onClick={onClick} sx={{ borderRadius: 2, mb: 0.5 }}>
      <ListItemIcon sx={{ minWidth: 42 }}>{icon}</ListItemIcon>
      <ListItemText primary={label} />
    </ListItemButton>
  )
}

function TaskList({
  tasks,
  onOpen,
  onCreate,
}: {
  tasks: Task[]
  onOpen: (id: string) => void
  onCreate: () => void
}) {
  return (
    <Stack spacing={3}>
      <Stack
        direction={{ xs: 'column', sm: 'row' }}
        sx={{ justifyContent: 'space-between', gap: 2 }}
      >
        <Box>
          <Typography variant="h4">备份任务</Typography>
          <Typography color="text.secondary">
            任务串行运行，每个任务内部按全局并发上传1—3个块。
          </Typography>
        </Box>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={onCreate}
          sx={{ alignSelf: { sm: 'center' } }}
        >
          创建任务
        </Button>
      </Stack>
      {tasks.length === 0 ? (
        <Card>
          <CardContent sx={{ py: 8, textAlign: 'center' }}>
            <BackupIcon color="disabled" sx={{ fontSize: 64 }} />
            <Typography variant="h6" sx={{ mt: 2 }}>
              还没有备份任务
            </Typography>
            <Typography color="text.secondary" sx={{ mb: 2 }}>
              创建新任务，或从已有WebDAV远端恢复。
            </Typography>
            <Button variant="contained" onClick={onCreate}>
              创建第一个任务
            </Button>
          </CardContent>
        </Card>
      ) : (
        <Box
          sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', lg: 'repeat(2, 1fr)' }, gap: 2 }}
        >
          {tasks.map((task) => (
            <Card key={task.id}>
              <CardActionArea onClick={() => onOpen(task.id)}>
                <CardContent>
                  <Stack direction="row" sx={{ justifyContent: 'space-between', gap: 2 }}>
                    <Box>
                      <Typography variant="h6">{task.name}</Typography>
                      <Typography variant="body2" color="text.secondary">
                        {task.sources.map((source) => source.alias).join('、')}
                      </Typography>
                    </Box>
                    <Chip
                      size="small"
                      color={
                        task.status === 'failed'
                          ? 'error'
                          : task.status === 'running'
                            ? 'primary'
                            : task.status === 'read_only'
                              ? 'warning'
                              : 'default'
                      }
                      label={statusLabel(task.status)}
                    />
                  </Stack>
                  <Stack direction="row" spacing={2} sx={{ mt: 3 }}>
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        模式
                      </Typography>
                      <Typography>
                        {task.mode === 'snapshot' ? `最近${task.retention}个版本` : '持续归档'}
                      </Typography>
                    </Box>
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        最大块
                      </Typography>
                      <Typography>{formatBytes(task.blockSize)}</Typography>
                    </Box>
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        最近运行
                      </Typography>
                      <Typography>{formatDate(task.lastRunAt)}</Typography>
                    </Box>
                  </Stack>
                </CardContent>
              </CardActionArea>
            </Card>
          ))}
        </Box>
      )}
    </Stack>
  )
}

function statusLabel(status: Task['status']) {
  return (
    {
      idle: '空闲',
      queued: '排队',
      running: '运行中',
      paused: '已暂停',
      failed: '失败',
      read_only: '只读',
      needs_input: '等待处理',
    } as Record<Task['status'], string>
  )[status]
}
