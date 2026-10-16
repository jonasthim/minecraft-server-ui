// Package content manages inert mod/plugin artifacts and bounded addon configuration.
// Publisher metadata and descriptor checks do not establish tested game support.
package content

import (
	"context"
	"database/sql"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"io"
)

const MaxArtifactBytes int64 = 2 << 30
const MaxConfigBytes = 1 << 20

type Catalog interface {
	Project(context.Context, catalog.Provider, string) (catalog.Project, error)
	Version(context.Context, catalog.Provider, string, string) (catalog.Version, error)
	Download(context.Context, catalog.Selection) (catalog.VerifiedArtifact, error)
}
type Instances interface {
	Get(context.Context, string) (instance.Detail, error)
	WithStoppedJob(context.Context, *jobs.Execution, func(*instance.StoppedSession) error) error
}
type RecoveryGuard interface {
	WithStoppedForRecovery(context.Context, string, func() error) error
}
type Config struct {
	DB        *sql.DB
	Paths     instance.Paths
	Instances Instances
	Queue     *jobs.Service
	Catalog   Catalog
	Recovery  RecoveryGuard
}
type Tuple struct {
	Minecraft     string `json:"minecraft"`
	Loader        string `json:"loader"`
	LoaderVersion string `json:"loader_version"`
}
type OwnedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Installed struct {
	ID           string               `json:"id"`
	Kind         catalog.Kind         `json:"kind"`
	Name         string               `json:"name"`
	Version      string               `json:"version"`
	Selection    *catalog.Selection   `json:"selection,omitempty"`
	SHA256       string               `json:"sha256"`
	Files        []OwnedFile          `json:"files"`
	Enabled      bool                 `json:"enabled"`
	Pinned       bool                 `json:"pinned"`
	Unknown      bool                 `json:"unknown"`
	TrialTuple   *Tuple               `json:"trial_tuple,omitempty"`
	Descriptor   Descriptor           `json:"descriptor"`
	Dependencies []catalog.Dependency `json:"dependencies"`
}
type Manifest struct {
	Revision          string      `json:"revision"`
	Tuple             Tuple       `json:"tuple"`
	Items             []Installed `json:"items"`
	AutomaticBlockers []string    `json:"automatic_blockers"`
}
type Mutation struct {
	Action         string             `json:"action"`
	ContentID      string             `json:"content_id,omitempty"`
	Kind           catalog.Kind       `json:"kind,omitempty"`
	Selection      *catalog.Selection `json:"selection,omitempty"`
	UploadID       string             `json:"upload_id,omitempty"`
	Trial          bool               `json:"trial,omitempty"`
	ConfigPath     string             `json:"config_path,omitempty"`
	ExpectedSHA256 string             `json:"expected_sha256,omitempty"`
	Text           string             `json:"text,omitempty"`
}
type ManualItem struct {
	Selection   catalog.Selection `json:"selection"`
	Filename    string            `json:"filename"`
	OfficialURL string            `json:"official_url"`
	Hashes      map[string]string `json:"hashes"`
	Reason      string            `json:"reason"`
}
type Preview struct {
	ID           string               `json:"id"`
	InstanceID   string               `json:"instance_id"`
	Tuple        Tuple                `json:"tuple"`
	Revision     string               `json:"revision"`
	Action       string               `json:"action"`
	Changes      []OwnedFile          `json:"changes"`
	Blockers     []string             `json:"blockers"`
	Dependencies []catalog.Dependency `json:"dependencies"`
	Manual       *ManualItem          `json:"manual,omitempty"`
	Unknown      bool                 `json:"unknown"`
}
type Upload struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}
type ConfigFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}
type ConfigText struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Text   string `json:"text"`
}
type Descriptor struct {
	ID                string            `json:"id"`
	Version           string            `json:"version"`
	Loader            string            `json:"loader"`
	Minecraft         string            `json:"minecraft"`
	LoaderRequirement string            `json:"loader_requirement"`
	ClientOnly        bool              `json:"client_only"`
	Required          map[string]string `json:"required"`
	Conflicts         map[string]string `json:"conflicts"`
	Unknown           bool              `json:"unknown"`
}
type privatePreview struct {
	Preview    Preview         `json:"preview"`
	Mutation   Mutation        `json:"mutation"`
	ActorID    int64           `json:"actor_id"`
	Filename   string          `json:"filename"`
	StageID    string          `json:"stage_id"`
	Metadata   string          `json:"metadata"`
	Next       *Installed      `json:"next,omitempty"`
	Operations []fileOperation `json:"operations"`
}
type fileOperation struct {
	Path        string `json:"path"`
	Before      string `json:"before"`
	After       string `json:"after"`
	BeforeStage string `json:"before_stage"`
	AfterStage  string `json:"after_stage"`
}
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func tuple(i domain.Instance) Tuple {
	return Tuple{i.MinecraftVersion, string(i.Flavor), i.LoaderVersion}
}
