import SaveIcon from '@mui/icons-material/Save'
import { Alert, Button, Card, CardContent, Stack, TextField, Typography } from '@mui/material'
import { useEffect, useState } from 'react'
import { api, body } from '../api'
import type { Settings } from '../types'

interface Props {
  notify: (message: string, severity?: 'success' | 'error') => void
}

export function SettingsPage({ notify }: Props) {
  const [settings, setSettings] = useState<Settings>({
    uploadConcurrency: 1,
    uploadLimitMiB: 0,
    downloadLimitMiB: 0,
    timezone: 'Asia/Singapore',
  })
  useEffect(() => {
    api<Settings>('/api/settings')
      .then(setSettings)
      .catch((reason: Error) => notify(reason.message, 'error'))
  }, [notify])
  const save = async () => {
    try {
      await api('/api/settings', { method: 'PUT', ...body(settings) })
      notify('全局设置已保存')
    } catch (reason) {
      notify(reason instanceof Error ? reason.message : '保存失败', 'error')
    }
  }
  return (
    <Stack spacing={3}>
      <div>
        <Typography variant="h4">全局设置</Typography>
        <Typography color="text.secondary">
          同一时间只执行一个任务，上传并发作用于该任务内部的数据块。
        </Typography>
      </div>
      <Card>
        <CardContent>
          <Stack spacing={2}>
            <TextField
              label="上传并发（1—3）"
              type="number"
              value={settings.uploadConcurrency}
              onChange={(event) =>
                setSettings({ ...settings, uploadConcurrency: Number(event.target.value) })
              }
              slotProps={{ htmlInput: { min: 1, max: 3 } }}
            />
            <TextField
              label="上传总限速（MiB/s，0不限）"
              type="number"
              value={settings.uploadLimitMiB}
              onChange={(event) =>
                setSettings({ ...settings, uploadLimitMiB: Number(event.target.value) })
              }
            />
            <TextField
              label="下载总限速（MiB/s，0不限）"
              type="number"
              value={settings.downloadLimitMiB}
              onChange={(event) =>
                setSettings({ ...settings, downloadLimitMiB: Number(event.target.value) })
              }
            />
            <TextField
              label="计划任务时区"
              value={settings.timezone}
              onChange={(event) => setSettings({ ...settings, timezone: event.target.value })}
            />
            <Alert severity="info">
              手动恢复本地已下载块不受下载限速影响。错过的计划不会在NAS启动后补执行。
            </Alert>
            <Button
              variant="contained"
              startIcon={<SaveIcon />}
              onClick={() => void save()}
              sx={{ alignSelf: 'flex-start' }}
            >
              保存
            </Button>
          </Stack>
        </CardContent>
      </Card>
    </Stack>
  )
}
