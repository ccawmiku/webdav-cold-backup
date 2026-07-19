package model

import "time"

const (
	FormatVersion       = 1
	DefaultRetention    = 3
	DefaultStablePeriod = 10 * time.Minute
)

type TaskMode string

const (
	TaskModeSnapshot TaskMode = "snapshot"
	TaskModeArchive  TaskMode = "archive"
)

type ScheduleType string

const (
	ScheduleManual ScheduleType = "manual"
	ScheduleDaily  ScheduleType = "daily"
	ScheduleWeekly ScheduleType = "weekly"
)

type TaskStatus string

const (
	TaskIdle       TaskStatus = "idle"
	TaskQueued     TaskStatus = "queued"
	TaskRunning    TaskStatus = "running"
	TaskPaused     TaskStatus = "paused"
	TaskFailed     TaskStatus = "failed"
	TaskReadOnly   TaskStatus = "read_only"
	TaskNeedsInput TaskStatus = "needs_input"
)

type RunStatus string

const (
	RunQueued     RunStatus = "queued"
	RunRunning    RunStatus = "running"
	RunComplete   RunStatus = "complete"
	RunIncomplete RunStatus = "incomplete"
	RunFailed     RunStatus = "failed"
	RunPaused     RunStatus = "paused"
)

type SourceRoot struct {
	Path  string `json:"path"`
	Alias string `json:"alias"`
}

type Schedule struct {
	Type    ScheduleType `json:"type"`
	Weekday time.Weekday `json:"weekday,omitempty"`
	Hour    int          `json:"hour,omitempty"`
	Minute  int          `json:"minute,omitempty"`
}

type WebDAVConfig struct {
	Endpoint string `json:"endpoint"`
	Root     string `json:"root"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type Task struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Mode             TaskMode     `json:"mode"`
	Password         string       `json:"password,omitempty"`
	Salt             string       `json:"salt"`
	Sources          []SourceRoot `json:"sources"`
	Remote           WebDAVConfig `json:"remote"`
	BlockSize        int64        `json:"blockSize"`
	Retention        int          `json:"retention"`
	Schedule         Schedule     `json:"schedule"`
	Status           TaskStatus   `json:"status"`
	CreatedAt        time.Time    `json:"createdAt"`
	UpdatedAt        time.Time    `json:"updatedAt"`
	LastRunAt        *time.Time   `json:"lastRunAt,omitempty"`
	LastScheduleKey  string       `json:"lastScheduleKey,omitempty"`
	AttachedWritable bool         `json:"attachedWritable"`
}

type PublicTask struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Mode             TaskMode     `json:"mode"`
	Sources          []SourceRoot `json:"sources"`
	Remote           WebDAVConfig `json:"remote"`
	BlockSize        int64        `json:"blockSize"`
	Retention        int          `json:"retention"`
	Schedule         Schedule     `json:"schedule"`
	Status           TaskStatus   `json:"status"`
	CreatedAt        time.Time    `json:"createdAt"`
	UpdatedAt        time.Time    `json:"updatedAt"`
	LastRunAt        *time.Time   `json:"lastRunAt,omitempty"`
	AttachedWritable bool         `json:"attachedWritable"`
}

func (t Task) Public() PublicTask {
	remote := t.Remote
	remote.Password = ""
	return PublicTask{
		ID: t.ID, Name: t.Name, Mode: t.Mode, Sources: t.Sources, Remote: remote,
		BlockSize: t.BlockSize, Retention: t.Retention, Schedule: t.Schedule,
		Status: t.Status, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		LastRunAt: t.LastRunAt, AttachedWritable: t.AttachedWritable,
	}
}

type TaskDescriptor struct {
	FormatVersion int       `json:"formatVersion"`
	TaskID        string    `json:"taskId"`
	Name          string    `json:"name"`
	Mode          TaskMode  `json:"mode"`
	Salt          string    `json:"salt"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type FileTimes struct {
	Modified time.Time `json:"modified"`
	Created  time.Time `json:"created"`
}

type BlockRef struct {
	ObjectPath string `json:"objectPath"`
	ObjectID   string `json:"objectId"`
	GroupID    string `json:"groupId"`
	Part       int    `json:"part"`
	TotalParts int    `json:"totalParts"`
	Offset     int64  `json:"offset"`
	Length     int64  `json:"length"`
	FileOffset int64  `json:"fileOffset"`
	ObjectSize int64  `json:"objectSize"`
	ObjectHash string `json:"objectHash"`
}

type FileEntry struct {
	ID             string     `json:"id"`
	RootAlias      string     `json:"rootAlias"`
	RelativePath   string     `json:"relativePath"`
	HistoricalPath []string   `json:"historicalPath,omitempty"`
	Size           int64      `json:"size"`
	Hash           string     `json:"hash"`
	Times          FileTimes  `json:"times"`
	Blocks         []BlockRef `json:"blocks"`
	MissingReason  string     `json:"missingReason,omitempty"`
}

type ObjectRecord struct {
	Path       string `json:"path"`
	ID         string `json:"id"`
	GroupID    string `json:"groupId"`
	Part       int    `json:"part"`
	TotalParts int    `json:"totalParts"`
	Size       int64  `json:"size"`
	Hash       string `json:"hash"`
}

type Snapshot struct {
	ID           string         `json:"id"`
	TaskID       string         `json:"taskId"`
	CreatedAt    time.Time      `json:"createdAt"`
	Complete     bool           `json:"complete"`
	Locked       bool           `json:"locked"`
	LockNote     string         `json:"lockNote,omitempty"`
	Sources      []SourceRoot   `json:"sources"`
	Files        []FileEntry    `json:"files"`
	Objects      []ObjectRecord `json:"objects"`
	MissingFiles []string       `json:"missingFiles,omitempty"`
	ChangeCount  int            `json:"changeCount"`
}

type TaskCatalog struct {
	FormatVersion int               `json:"formatVersion"`
	TaskID        string            `json:"taskId"`
	Name          string            `json:"name"`
	Mode          TaskMode          `json:"mode"`
	BlockSize     int64             `json:"blockSize"`
	Retention     int               `json:"retention"`
	Schedule      Schedule          `json:"schedule"`
	Sources       []SourceRoot      `json:"sources"`
	Snapshots     []SnapshotSummary `json:"snapshots,omitempty"`
	Archive       *Snapshot         `json:"archive,omitempty"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

type SnapshotSummary struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Complete  bool      `json:"complete"`
	Locked    bool      `json:"locked"`
	FileCount int       `json:"fileCount"`
	Size      int64     `json:"size"`
}

type ObjectPayloadMetadata struct {
	FormatVersion int                 `json:"formatVersion"`
	TaskID        string              `json:"taskId"`
	ObjectID      string              `json:"objectId"`
	GroupID       string              `json:"groupId"`
	Part          int                 `json:"part"`
	TotalParts    int                 `json:"totalParts"`
	Kind          string              `json:"kind"`
	Files         []PayloadFileRecord `json:"files"`
}

type PayloadFileRecord struct {
	FileID       string    `json:"fileId"`
	RootAlias    string    `json:"rootAlias"`
	RelativePath string    `json:"relativePath"`
	Size         int64     `json:"size"`
	Hash         string    `json:"hash"`
	Times        FileTimes `json:"times"`
	Offset       int64     `json:"offset"`
	Length       int64     `json:"length"`
	FileOffset   int64     `json:"fileOffset"`
}

type RunRecord struct {
	ID            string     `json:"id"`
	TaskID        string     `json:"taskId"`
	Status        RunStatus  `json:"status"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	FilesScanned  int        `json:"filesScanned"`
	FilesAdded    int        `json:"filesAdded"`
	BytesUploaded int64      `json:"bytesUploaded"`
	Message       string     `json:"message,omitempty"`
	Details       []string   `json:"details,omitempty"`
}

type GlobalSettings struct {
	UploadConcurrency int    `json:"uploadConcurrency"`
	UploadLimitMiB    int64  `json:"uploadLimitMiB"`
	DownloadLimitMiB  int64  `json:"downloadLimitMiB"`
	Timezone          string `json:"timezone"`
}

func DefaultSettings() GlobalSettings {
	return GlobalSettings{UploadConcurrency: 1, Timezone: "Asia/Singapore"}
}
