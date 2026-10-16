// Package metrics records bounded exact-JVM and instance-owned observations.
package metrics

import (
	"context"
	"database/sql"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"sync"
	"time"
)

const Interval = 30 * time.Second
const RawRetention = 7 * 24 * time.Hour
const HourRetention = 90 * 24 * time.Hour
const MaxPoints = 1000

type Field string

const (
	CPU      Field = "cpu"
	Resident Field = "resident"
	Private  Field = "private"
	Disk     Field = "disk"
	Players  Field = "players"
)

type SourceKind string

const (
	SourceLinuxProc      SourceKind = "linux_proc"
	SourceWindowsProcess SourceKind = "windows_process_api"
	SourceJVMIdentity    SourceKind = "jvm_identity"
	SourceInstanceDisk   SourceKind = "instance_logical_bytes"
	SourcePlayers        SourceKind = "task11_native_roster"
	SourceSampling       SourceKind = "sampling"
)

type Source struct {
	Field      Field      `json:"field"`
	Kind       SourceKind `json:"kind"`
	ObservedAt time.Time  `json:"observed_at"`
	GapReason  string     `json:"gap_reason,omitempty"`
}
type Counts struct {
	Samples  int `json:"samples"`
	CPU      int `json:"cpu"`
	Resident int `json:"resident"`
	Private  int `json:"private"`
	Disk     int `json:"disk"`
	Players  int `json:"players"`
}
type Point struct {
	CollectedAt   time.Time `json:"collected_at"`
	CPUPercent    *float64  `json:"cpu_percent"`
	ResidentBytes *int64    `json:"resident_bytes"`
	PrivateBytes  *int64    `json:"private_bytes"`
	DiskBytes     *int64    `json:"disk_bytes"`
	Players       *float64  `json:"players"`
	Counts        Counts    `json:"counts"`
	Expected      int       `json:"expected"`
	Gap           bool      `json:"gap"`
	Sources       []Source  `json:"sources"`
}
type Query struct {
	InstanceID string
	From, To   time.Time
	Resolution string
}
type Page struct {
	InstanceID      string  `json:"instance_id"`
	Resolution      string  `json:"resolution"`
	IntervalSeconds int     `json:"interval_seconds"`
	Points          []Point `json:"points"`
	CPUConvention   string  `json:"cpu_convention"`
}
type Native interface {
	Identity(context.Context, string) (domain.ProcessIdentity, error)
	SampleMetrics(context.Context, domain.ProcessIdentity) (launcher.ProcessCounters, error)
	Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error)
}
type Instances interface {
	List(context.Context) ([]instance.Detail, error)
	Get(context.Context, string) (instance.Detail, error)
}
type PlayerSource interface {
	Snapshot(context.Context, string) (console.PlayerSnapshot, error)
}
type Config struct {
	DB        *sql.DB
	Paths     instance.Paths
	Instances Instances
	Native    Native
	Players   PlayerSource
	Now       func() time.Time
}
type previous struct {
	identity domain.ProcessIdentity
	cpu      int64
	at       time.Time
	wall     time.Time
}
type Service struct {
	cfg        Config
	previousMu sync.Mutex
	previous   map[string]previous
}

func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, previous: map[string]previous{}}
}
