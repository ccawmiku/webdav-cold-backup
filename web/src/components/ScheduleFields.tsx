import { FormControl, InputLabel, MenuItem, Select, Stack, TextField } from '@mui/material'
import type { Schedule } from '../types'

interface Props {
  value: Schedule
  onChange: (value: Schedule) => void
}

export function ScheduleFields({ value, onChange }: Props) {
  const time = `${String(value.hour ?? 0).padStart(2, '0')}:${String(value.minute ?? 0).padStart(2, '0')}`
  return (
    <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
      <FormControl fullWidth>
        <InputLabel>运行周期</InputLabel>
        <Select
          label="运行周期"
          value={value.type}
          onChange={(event) => onChange({ ...value, type: event.target.value as Schedule['type'] })}
        >
          <MenuItem value="manual">仅手动</MenuItem>
          <MenuItem value="daily">每天</MenuItem>
          <MenuItem value="weekly">每周</MenuItem>
        </Select>
      </FormControl>
      {value.type === 'weekly' && (
        <FormControl fullWidth>
          <InputLabel>星期</InputLabel>
          <Select
            label="星期"
            value={value.weekday ?? 0}
            onChange={(event) => onChange({ ...value, weekday: Number(event.target.value) })}
          >
            {['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'].map(
              (label, index) => (
                <MenuItem key={label} value={index}>
                  {label}
                </MenuItem>
              ),
            )}
          </Select>
        </FormControl>
      )}
      {value.type !== 'manual' && (
        <TextField
          fullWidth
          label="执行时间"
          type="time"
          value={time}
          onChange={(event) => {
            const [hour, minute] = event.target.value.split(':').map(Number)
            onChange({ ...value, hour, minute })
          }}
          slotProps={{ inputLabel: { shrink: true } }}
        />
      )}
    </Stack>
  )
}
