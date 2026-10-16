package modpack

import (
	"context"
	"database/sql"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"io"
)

type Catalog interface {
	Project(context.Context, catalog.Provider, string) (catalog.Project, error)
	Version(context.Context, catalog.Provider, string, string) (catalog.Version, error)
	Download(context.Context, catalog.Selection) (catalog.VerifiedArtifact, error)
}
type Instances interface {
	Get(context.Context, string) (instance.Detail, error)
	WithStoppedJob(context.Context, *jobs.Execution, func(*instance.StoppedSession) error) error
}
type Recovery interface {
	WithStoppedForRecovery(context.Context, string, func() error) error
}
type ContentService interface {
	Snapshot(context.Context, string) (content.Manifest, error)
	PreparePackCandidate(context.Context, string, content.Tuple, []content.Installed) ([]content.Installed, []string, error)
	WithVerifiedPackUpload(context.Context, string, string, domain.User, map[string]string, func(io.ReaderAt, int64) error) error
	WithPackParticipant(context.Context, *instance.StoppedSession, content.Manifest, []content.Installed, domain.User, func(context.Context, instance.Detail, instance.Directories, *content.PackParticipant) error) error
}
type Config struct {
	DB        *sql.DB
	Paths     instance.Paths
	Instances Instances
	Queue     *jobs.Service
	Catalog   Catalog
	Content   *content.Service
	Recovery  Recovery
}
type Request struct {
	ArchiveReceiptID  string               `json:"archive_receipt_id,omitempty"`
	PreviousPreviewID string               `json:"previous_preview_id,omitempty"`
	Pack              catalog.Selection    `json:"pack"`
	Choices           map[string]string    `json:"choices"`
	Selections        map[string]Selection `json:"selections"`
	Manual            map[string]string    `json:"manual"`
}
type ManualFile struct {
	Path        string            `json:"path"`
	OfficialURL string            `json:"official_url"`
	Hashes      map[string]string `json:"hashes"`
	Reason      string            `json:"reason"`
}
type Source struct {
	ArtifactSelection catalog.Selection  `json:"artifact_selection"`
	Selection         catalog.Selection  `json:"selection"`
	OfficialURL       string             `json:"official_url"`
	Name              string             `json:"name"`
	SHA256            string             `json:"sha256"`
	Provenance        catalog.Provenance `json:"provenance"`
}
type Baseline struct {
	Source     Source               `json:"source"`
	Revision   string               `json:"revision"`
	Files      map[string]string    `json:"files"`
	Selections map[string]Selection `json:"selections"`
	Content    map[string]string    `json:"content"`
}
type ArchiveManual struct {
	Selection   catalog.Selection `json:"selection"`
	Filename    string            `json:"filename"`
	Size        int64             `json:"size"`
	Hashes      map[string]string `json:"hashes"`
	OfficialURL string            `json:"official_url"`
	Reason      string            `json:"reason"`
}
type ArchiveReceipt struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Preview struct {
	ArchiveManual    *ArchiveManual       `json:"archive_manual,omitempty"`
	ID               string               `json:"id"`
	InstanceID       string               `json:"instance_id"`
	Tuple            content.Tuple        `json:"tuple"`
	Revision         string               `json:"revision"`
	BaselineRevision string               `json:"baseline_revision"`
	Source           Source               `json:"source"`
	Changes          []Change             `json:"changes"`
	Selections       map[string]Selection `json:"selections"`
	Manual           []ManualFile         `json:"manual"`
	Blockers         []string             `json:"blockers"`
	Excluded         []string             `json:"excluded"`
	Summary          string               `json:"summary"`
}
type Snapshot struct {
	Baseline *Baseline `json:"baseline,omitempty"`
	Blockers []string  `json:"blockers"`
}
type Upload struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type stage struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type operation struct {
	Path   string `json:"path"`
	Before stage  `json:"before"`
	After  stage  `json:"after"`
}
type privatePreview struct {
	Members    map[string]PackFile `json:"members"`
	Preview    Preview             `json:"preview"`
	Request    Request             `json:"request"`
	ActorID    int64               `json:"actor_id"`
	Metadata   string              `json:"metadata"`
	Manifest   content.Manifest    `json:"manifest"`
	Candidate  []content.Installed `json:"candidate"`
	Baseline   Baseline            `json:"baseline"`
	Operations []operation         `json:"operations"`
}
