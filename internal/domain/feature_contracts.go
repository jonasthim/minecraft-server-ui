package domain

import (
	"context"
	"encoding/json"
	"time"
)

// Event is the public stream envelope shared with internal/events. Job contains
// only a serialized public job projection; durable job inputs never enter it.
// Type identifies the payload; consumers must tolerate unfamiliar future types.
type Event struct {
	Type  string          `json:"type"`
	JobID string          `json:"job_id,omitempty"`
	Job   json.RawMessage `json:"job,omitempty"`
	Log   *EventLog       `json:"log,omitempty"`
}
type EventLog struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	CreatedAt time.Time `json:"created_at"`
	Message   string    `json:"message"`
}

type BackupKind string

const (
	BackupWorld BackupKind = "world"
	BackupFull  BackupKind = "full"
)

type BackupTrigger string

const (
	BackupManual    BackupTrigger = "manual"
	BackupAutomatic BackupTrigger = "automatic"
	BackupSafety    BackupTrigger = "safety"
)

type BackupState string

const (
	BackupStaging BackupState = "staging"
	BackupReady   BackupState = "ready"
	BackupFailed  BackupState = "failed"
)

// Backup is recorded ready only after archive validation. Paths are private to
// the backup service; clients identify archives by ID. Warnings disclose live
// player/mod consistency limits. Automatic retention never removes protected
// records (manual backups are protected by policy).
type Backup struct {
	ID          string        `json:"id"`
	InstanceID  string        `json:"instance_id"`
	Kind        BackupKind    `json:"kind"`
	Trigger     BackupTrigger `json:"trigger"`
	State       BackupState   `json:"state"`
	Protected   bool          `json:"protected"`
	SizeBytes   int64         `json:"size_bytes"`
	Checksum    Checksum      `json:"checksum"`
	CreatedAt   time.Time     `json:"created_at"`
	ValidatedAt *time.Time    `json:"validated_at,omitempty"`
	Warnings    []string      `json:"warnings"`
}
type BackupRequest struct {
	InstanceID string        `json:"instance_id"`
	Kind       BackupKind    `json:"kind"`
	Trigger    BackupTrigger `json:"trigger"`
}
type RestoreRequest struct {
	InstanceID string `json:"instance_id"`
	BackupID   string `json:"backup_id"`
	Confirmed  bool   `json:"confirmed"`
}

// SaveLease belongs to the online copy stage only, capped at five minutes. The
// implementation must acknowledge flush, always attempt save-on, and reconcile
// outstanding leases after restart before accepting another online copy.
type SaveLease struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// These methods enqueue durable operations and return their job IDs. Restore
// must verify explicit confirmation/stopped state and create its safety backup.
type BackupService interface {
	List(context.Context, string) ([]Backup, error)
	EnqueueBackup(context.Context, BackupRequest) (string, error)
	EnqueueRestore(context.Context, RestoreRequest) (string, error)
}
type SaveLeaseService interface {
	Acquire(context.Context, string) (SaveLease, error)
	Release(context.Context, SaveLease) error
	Reconcile(context.Context) error
}

type ScheduleAction string

const (
	ScheduleBackup       ScheduleAction = "backup"
	ScheduleCommand      ScheduleAction = "command"
	ScheduleAnnouncement ScheduleAction = "announcement"
	ScheduleRestart      ScheduleAction = "restart"
	ScheduleUpdate       ScheduleAction = "update"
)

// Expression is a cron expression interpreted in the explicit IANA timezone.
// Update schedules use the instance's pinned version. CountdownSeconds applies
// to restart announcements and defaults to 300 in the scheduler implementation.
type Schedule struct {
	ID               string         `json:"id"`
	InstanceID       string         `json:"instance_id"`
	Name             string         `json:"name"`
	Enabled          bool           `json:"enabled"`
	Action           ScheduleAction `json:"action"`
	Expression       string         `json:"expression"`
	Timezone         string         `json:"timezone"`
	Command          string         `json:"command,omitempty"`
	Announcement     string         `json:"announcement,omitempty"`
	BackupKind       BackupKind     `json:"backup_kind,omitempty"`
	CountdownSeconds int            `json:"countdown_seconds"`
	NextRunAt        *time.Time     `json:"next_run_at,omitempty"`
}
type ScheduleService interface {
	List(context.Context, string) ([]Schedule, error)
	Save(context.Context, Schedule) (Schedule, error)
	Delete(context.Context, string) error
}

// TelemetrySource describes how an observation was obtained and why a value may
// be absent. Missing metrics use nil; zero is a real measured value.
type TelemetrySource struct {
	Kind       string    `json:"kind"`
	ObservedAt time.Time `json:"observed_at"`
	GapReason  string    `json:"gap_reason,omitempty"`
}
type MetricSample struct {
	InstanceID    string            `json:"instance_id"`
	CollectedAt   time.Time         `json:"collected_at"`
	CPUPercent    *float64          `json:"cpu_percent"`
	ResidentBytes *int64            `json:"resident_bytes"`
	HeapUsedBytes *int64            `json:"heap_used_bytes"`
	HeapMaxBytes  *int64            `json:"heap_max_bytes"`
	DiskBytes     *int64            `json:"disk_bytes"`
	Players       *int              `json:"players"`
	Sources       []TelemetrySource `json:"sources"`
}
type MetricResolution string

const (
	MetricsRaw    MetricResolution = "raw"
	MetricsHourly MetricResolution = "hourly"
)

type MetricsQuery struct {
	InstanceID string
	From       time.Time
	To         time.Time
	Resolution MetricResolution
}

// Query returns authorized JVM/instance summaries. Collection and retention
// policies (30s / 7d raw / 90d hourly) are the metrics package's responsibility.
type MetricsService interface {
	Query(context.Context, MetricsQuery) ([]MetricSample, error)
}

type CatalogProvider string

const (
	CatalogModrinth   CatalogProvider = "modrinth"
	CatalogCurseForge CatalogProvider = "curseforge"
	CatalogHangar     CatalogProvider = "hangar"
)

type CatalogKind string

const (
	CatalogMod      CatalogKind = "mod"
	CatalogPlugin   CatalogKind = "plugin"
	CatalogModpack  CatalogKind = "modpack"
	CatalogDatapack CatalogKind = "datapack"
)

type CatalogQuery struct {
	Provider  CatalogProvider  `json:"provider"`
	Kind      CatalogKind      `json:"kind"`
	Query     string           `json:"query"`
	Selection VersionSelection `json:"selection"`
	Cursor    string           `json:"cursor,omitempty"`
	Limit     int              `json:"limit"`
}
type CatalogProject struct {
	Provider    CatalogProvider `json:"provider"`
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Kind        CatalogKind     `json:"kind"`
	URL         string          `json:"url"`
}
type CatalogPage struct {
	Projects   []CatalogProject `json:"projects"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// Declared compatibility is publisher metadata, not tested support evidence.
type CatalogVersion struct {
	Provider                  CatalogProvider `json:"provider"`
	ProjectID                 string          `json:"project_id"`
	ID                        string          `json:"id"`
	Name                      string          `json:"name"`
	DeclaredMinecraftVersions []string        `json:"declared_minecraft_versions"`
	DeclaredFlavors           []Flavor        `json:"declared_flavors"`
	Artifacts                 []Artifact      `json:"artifacts"`
	RequiredProjectIDs        []string        `json:"required_project_ids"`
}

// Credentials remain private provider configuration. In particular, CurseForge
// is unavailable until an operator supplies a key; none travels in these DTOs.
type CatalogService interface {
	Search(context.Context, CatalogQuery) (CatalogPage, error)
	Versions(context.Context, CatalogProvider, string, VersionSelection) ([]CatalogVersion, error)
}
type ArtifactStore interface {
	Fetch(context.Context, Artifact) (InstalledArtifact, error)
}
