package domain

import (
	"context"
	"io"
	"time"
)

// Availability carries a reason when a feature is unavailable. Providers derive
// capabilities from the selected version and actual installed artifacts.
type Availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}
type Capabilities struct {
	Console    Availability `json:"console"`
	RCON       Availability `json:"rcon"`
	Plugins    Availability `json:"plugins"`
	Mods       Availability `json:"mods"`
	Datapacks  Availability `json:"datapacks"`
	LiveBackup Availability `json:"live_backup"`
	Map        Availability `json:"map"`
	Metrics    Availability `json:"metrics"`
}

type Checksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// Artifact records provider provenance and its expected published digest.
// SourceURL is metadata; callers must still enforce their provider URL policy.
type Artifact struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	ProjectID   string    `json:"project_id,omitempty"`
	VersionID   string    `json:"version_id"`
	Filename    string    `json:"filename"`
	SourceURL   string    `json:"source_url"`
	Size        int64     `json:"size"`
	Checksum    Checksum  `json:"checksum"`
	RetrievedAt time.Time `json:"retrieved_at"`
}

type JavaRuntime struct {
	ID           string   `json:"id"`
	Vendor       string   `json:"vendor"`
	Major        int      `json:"major"`
	Version      string   `json:"version"`
	OS           string   `json:"os"`
	Architecture string   `json:"architecture"`
	Executable   string   `json:"executable"`
	Artifact     Artifact `json:"artifact"`
}

type VersionSelection struct {
	Flavor           Flavor `json:"flavor"`
	MinecraftVersion string `json:"minecraft_version"`
	LoaderVersion    string `json:"loader_version,omitempty"`
}

type LaunchRequirements struct {
	JavaMajor         int  `json:"java_major"`
	MinJavaMajor      int  `json:"min_java_major"`
	MaxJavaMajor      int  `json:"max_java_major,omitempty"`
	RequiresInstaller bool `json:"requires_installer"`
}

// LaunchSpec is an argv contract, never a shell command. Env contains only
// explicit overrides. Credentials must not be serialized into API responses.
type LaunchSpec struct {
	Executable    string            `json:"executable"`
	Args          []string          `json:"args"`
	WorkingDir    string            `json:"working_dir"`
	Env           map[string]string `json:"-"`
	JavaRuntimeID string            `json:"java_runtime_id"`
	JavaChecksum  Checksum          `json:"java_checksum"`
}

// ProcessIdentity must be checked before operating on a persisted PID. PID alone
// is unsafe after a manager restart because the OS can reuse it. PID is the JVM,
// while LauncherPID is diagnostic metadata when an intermediate launcher exists.
type ProcessIdentity struct {
	InstanceID    string    `json:"instance_id"`
	PID           int       `json:"pid"`
	LauncherPID   int       `json:"launcher_pid,omitempty"`
	StartIdentity string    `json:"start_identity"`
	Executable    string    `json:"executable"`
	WorkingDir    string    `json:"working_dir"`
	JavaRuntimeID string    `json:"java_runtime_id"`
	JavaChecksum  Checksum  `json:"java_checksum"`
	StartedAt     time.Time `json:"started_at"`
}

type InstallPlan struct {
	Selection    VersionSelection   `json:"selection"`
	Requirements LaunchRequirements `json:"requirements"`
	Artifacts    []Artifact         `json:"artifacts"`
	Capabilities Capabilities       `json:"capabilities"`
	Warnings     []string           `json:"warnings"`
}

type ContentKind string

const (
	ContentMod      ContentKind = "mod"
	ContentPlugin   ContentKind = "plugin"
	ContentDatapack ContentKind = "datapack"
)

type ContentChange struct {
	Kind        ContentKind `json:"kind"`
	Artifact    Artifact    `json:"artifact"`
	Destination string      `json:"destination"`
	Remove      bool        `json:"remove"`
}

// Plans must be reviewed before applying; dependencies and incompatibilities
// are explicit so a partial dependency install cannot be presented as success.
type ContentPlan struct {
	InstanceID      string          `json:"instance_id"`
	Changes         []ContentChange `json:"changes"`
	Warnings        []string        `json:"warnings"`
	Blockers        []string        `json:"blockers"`
	RequiresRestart bool            `json:"requires_restart"`
}

type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

type Job struct {
	ID         string     `json:"id"`
	InstanceID string     `json:"instance_id,omitempty"`
	Kind       string     `json:"kind"`
	State      JobState   `json:"state"`
	Phase      string     `json:"phase"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Narrow interfaces let providers, native process supervision and persistence be
// implemented and tested independently. No interface implies a working feature.
type InstanceRepository interface {
	List(context.Context) ([]Instance, error)
	Get(context.Context, string) (Instance, error)
	Create(context.Context, Instance, InstanceConfig) error
	Update(context.Context, Instance) error
	Delete(context.Context, string) error
}
type DistributionProvider interface {
	Flavor() Flavor
	Resolve(context.Context, VersionSelection) (InstallPlan, error)
	// Install accepts a validated instance ID, not a caller supplied directory.
	// Providers derive canonical paths and return an activation proposal; the
	// lifecycle coordinator owns stopped-state, backup, EULA and persistence gates.
	Install(ctx context.Context, instanceID string, plan InstallPlan, runtime JavaRuntime, progress io.Writer) (LaunchSpec, error)
}
type ProcessSupervisor interface {
	Start(context.Context, string, LaunchSpec) (ProcessIdentity, error)
	Stop(context.Context, ProcessIdentity) error
	Status(context.Context, ProcessIdentity) (InstanceState, error)
	Command(context.Context, ProcessIdentity, string) error
}

// InstalledArtifact distinguishes downloaded, verified bytes from catalog metadata.
type InstalledArtifact struct {
	Artifact         Artifact  `json:"artifact"`
	LocalPath        string    `json:"local_path"`
	VerifiedChecksum Checksum  `json:"verified_checksum"`
	VerifiedAt       time.Time `json:"verified_at"`
}

// CommandResult records whether the server acknowledged the command. A stdin
// write alone cannot verify completion of a save flush or another backup fence.
type CommandResult struct {
	Output   string `json:"output"`
	Received bool   `json:"received"`
	Verified bool   `json:"verified"`
}
type CommandExecutor interface {
	Execute(context.Context, string, string) (CommandResult, error)
}
