export type TaskMode = 'snapshot' | 'archive'
export type TaskStatus =
  'idle' | 'queued' | 'running' | 'paused' | 'failed' | 'read_only' | 'needs_input'

export interface SourceRoot {
  path: string
  alias: string
}

export interface Schedule {
  type: 'manual' | 'daily' | 'weekly'
  weekday?: number
  hour?: number
  minute?: number
}

export interface WebDAVConfig {
  endpoint: string
  root: string
  username?: string
  password?: string
}

export interface RemotePreset {
  id: string
  name: string
  remote: WebDAVConfig
  hasPassword: boolean
  createdAt: string
  updatedAt: string
}

export interface WebDAVSelection {
  remotePresetId: string
  remote: WebDAVConfig
}

export interface RemoteDirectory {
  path: string
  name: string
}

export interface Task {
  id: string
  name: string
  mode: TaskMode
  sources: SourceRoot[]
  remote: WebDAVConfig
  blockSize: number
  retention: number
  schedule: Schedule
  status: TaskStatus
  createdAt: string
  updatedAt: string
  lastRunAt?: string
  attachedWritable: boolean
}

export interface BlockRef {
  objectPath: string
  part: number
  totalParts: number
  length: number
}

export interface FileEntry {
  id: string
  rootAlias: string
  relativePath: string
  size: number
  hash: string
  times: { modified: string; created: string }
  blocks: BlockRef[]
  missingReason?: string
}

export interface Snapshot {
  id: string
  taskId: string
  createdAt: string
  complete: boolean
  locked: boolean
  lockNote?: string
  files: FileEntry[]
  missingFiles?: string[]
  changeCount: number
}

export interface RunRecord {
  id: string
  taskId: string
  status: string
  startedAt: string
  finishedAt?: string
  filesScanned: number
  filesAdded: number
  bytesUploaded: number
  message?: string
  details?: string[]
}

export interface TaskProgress {
  taskId: string
  phase: string
  percent: number
  message: string
  currentFile?: string
  filesProcessed: number
  filesTotal: number
  objectsCompleted: number
  objectsTotal: number
  bytesCompleted: number
  bytesTotal: number
  updatedAt: string
}

export interface Settings {
  uploadConcurrency: number
  uploadLimitMiB: number
  downloadLimitMiB: number
  timezone: string
}

export interface RuntimeInfo {
  mode: 'server' | 'offline'
  version: string
  platform: string
}

export interface FsItem {
  path: string
  name: string
}

export interface CheckIssue {
  path: string
  expected?: number
  actual?: number
  kind: string
}

export interface CheckResult {
  checked: number
  issues: CheckIssue[]
}

export interface OfflineOpenResult {
  task: Task
  snapshots: Array<{
    id: string
    createdAt: string
    complete: boolean
    locked: boolean
    fileCount: number
    size: number
  }>
  selected: string
  salvaged: boolean
}

export interface RestoreProgress {
  status: 'idle' | 'queued' | 'running' | 'completed' | 'failed'
  phase: string
  percent: number
  message: string
  currentObject?: string
  currentFile?: string
  filesCompleted: number
  filesTotal: number
  objectsChecked: number
  objectsCheckTotal: number
  objectsCompleted: number
  objectsTotal: number
  bytesCompleted: number
  bytesTotal: number
  verifyBytesCompleted: number
  verifyBytesTotal: number
  restoredFiles: number
  skippedFiles: number
  failedFiles: number
  verifiedFiles: number
  error?: string
  updatedAt: string
}
