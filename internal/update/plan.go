// Package update coordinates exact, previewed instance updates. Planning evidence
// does not establish a tested game/platform support claim.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"path"
	"strings"
	"time"
)

type Mode string

const (
	Manual    Mode = "manual"
	Automatic Mode = "automatic"
)

type ContentSelection = catalog.Selection

type Request struct {
	ContentManual     map[string]string            `json:"content_manual,omitempty"`
	Mode              Mode                         `json:"mode"`
	Selection         *domain.VersionSelection     `json:"selection,omitempty"`
	ContentSelections map[string]catalog.Selection `json:"content_selections,omitempty"`
	Pack              *modpack.Request             `json:"pack,omitempty"`
	CountdownSeconds  *int                         `json:"countdown_seconds,omitempty"`
}
type Snapshot struct {
	InstanceRevision string `json:"instance_revision"`
	Generation       string `json:"generation"`
	ContentRevision  string `json:"content_revision"`
	PackRevision     string `json:"pack_revision"`
}
type RuntimeIdentity struct {
	ID           string `json:"id"`
	Major        int    `json:"major"`
	Version      string `json:"version"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	SHA256       string `json:"sha256"`
}
type Artifact struct {
	Checksum domain.Checksum `json:"checksum"`
	Filename string          `json:"filename"`
	Size     int64           `json:"size"`
	SHA256   string          `json:"sha256"`
}
type Change struct {
	Component string `json:"component"`
	Before    string `json:"before"`
	After     string `json:"after"`
}
type Blocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Plan struct {
	ContentManualRequired map[string]content.ManualItem `json:"content_manual_required,omitempty"`
	Pack                  *modpack.Preview              `json:"pack,omitempty"`
	ID                    string                        `json:"id"`
	InstanceID            string                        `json:"instance_id"`
	ActorID               int64                         `json:"-"`
	Mode                  Mode                          `json:"mode"`
	Snapshot              Snapshot                      `json:"snapshot"`
	From                  domain.VersionSelection       `json:"from"`
	Target                domain.VersionSelection       `json:"target"`
	Requirements          domain.LaunchRequirements     `json:"requirements"`
	Runtime               RuntimeIdentity               `json:"runtime"`
	Artifacts             []Artifact                    `json:"artifacts"`
	Changes               []Change                      `json:"changes"`
	Blockers              []Blocker                     `json:"blockers"`
	CountdownSeconds      int                           `json:"countdown_seconds"`
	ExpiresAt             time.Time                     `json:"expires_at"`
}
type FrozenPlan struct {
	raw     []byte
	digest  string
	actorID int64
}

func invalid(message string) error  { return domain.E(domain.CodeValidationFailed, message) }
func conflict(message string) error { return domain.E(domain.CodeConflict, message) }
func exact(selection domain.VersionSelection) error {
	if _, err := managedruntime.Select(selection); err != nil {
		return invalid("exact reviewed Minecraft/flavor selection required")
	}
	if selection.Flavor == domain.FlavorVanilla && selection.LoaderVersion != "" {
		return invalid("Vanilla cannot select a loader")
	}
	if selection.Flavor != domain.FlavorVanilla && (selection.LoaderVersion == "" || len(selection.LoaderVersion) > 128 || strings.ContainsAny(selection.LoaderVersion, "/\\ :\t\r\n\x00")) {
		return invalid("exact loader selection required")
	}
	return nil
}
func (r Request) Resolve(current domain.VersionSelection) (domain.VersionSelection, int, error) {
	countdown := 300
	if r.CountdownSeconds != nil {
		countdown = *r.CountdownSeconds
	}
	if countdown < 0 || countdown > 3600 {
		return current, 0, invalid("countdown must be between 0 and 3600 seconds")
	}
	switch r.Mode {
	case Automatic:
		if r.Selection != nil || len(r.ContentSelections) > 0 || len(r.ContentManual) > 0 || r.Pack != nil {
			return current, 0, invalid("automatic update targets are derived from the current pinned instance")
		}
	case Manual:
		if r.Selection == nil {
			return current, 0, invalid("manual update requires an exact target selection")
		}
		current = *r.Selection
	default:
		return current, 0, invalid("choose manual or automatic update")
	}
	if err := exact(current); err != nil {
		return current, 0, err
	}
	if len(r.ContentSelections) > 2048 {
		return current, 0, invalid("content selection bound exceeded")
	}

	if len(r.ContentManual) > 2048 {
		return current, 0, invalid("content manual receipt bound exceeded")
	}
	for id, receipt := range r.ContentManual {
		if _, ok := r.ContentSelections[id]; !ok || !validHex(receipt, 32) {
			return current, 0, invalid("manual receipt requires an exact selected content item")
		}
	}
	for id, selection := range r.ContentSelections {
		if id == "" || len(id) > 256 || strings.ContainsAny(id, "/\\\r\n\x00") || selection.ProjectID == "" || selection.VersionID == "" || len(selection.ProjectID) > 256 || len(selection.VersionID) > 256 {
			return current, 0, invalid("invalid exact content selection")
		}
		if selection.Provider != catalog.Modrinth && selection.Provider != catalog.CurseForge && selection.Provider != catalog.Hangar {
			return current, 0, invalid("invalid content provider")
		}
	}
	return current, countdown, nil
}
func validHex(value string, size int) bool {
	_, err := hex.DecodeString(value)
	return len(value) == size && value == strings.ToLower(value) && err == nil
}
func Freeze(p Plan) (FrozenPlan, error) {
	if !validHex(p.ID, 32) || domain.ValidateInstanceID(p.InstanceID) != nil || p.ActorID <= 0 || p.ExpiresAt.IsZero() {
		return FrozenPlan{}, invalid("plan identity and expiry are required")
	}
	if p.Snapshot.InstanceRevision == "" || !validHex(p.Snapshot.Generation, 32) || !validHex(p.Snapshot.ContentRevision, 64) || p.Snapshot.PackRevision == "" {
		return FrozenPlan{}, invalid("complete original snapshot required")
	}
	if err := exact(p.From); err != nil {
		return FrozenPlan{}, err
	}
	if err := exact(p.Target); err != nil {
		return FrozenPlan{}, err
	}
	if p.Mode != Manual && p.Mode != Automatic {
		return FrozenPlan{}, invalid("invalid plan mode")
	}
	if p.Mode == Automatic && (p.From.MinecraftVersion != p.Target.MinecraftVersion || p.From.Flavor != p.Target.Flavor) {
		return FrozenPlan{}, conflict("automatic updates must preserve exact Minecraft version and flavor")
	}
	if p.CountdownSeconds < 0 || p.CountdownSeconds > 3600 || len(p.Artifacts) == 0 || len(p.Artifacts) > 4096 || len(p.Changes) > 16384 || len(p.Blockers) > 16384 {
		return FrozenPlan{}, invalid("plan bounds exceeded")
	}
	if p.Runtime.ID == "" || p.Runtime.Version == "" || p.Runtime.OS == "" || p.Runtime.Architecture != "x64" || !validHex(p.Runtime.SHA256, 64) {
		return FrozenPlan{}, invalid("verified runtime identity required")
	}
	if p.Requirements.JavaMajor != p.Runtime.Major || p.Runtime.Major < p.Requirements.MinJavaMajor || p.Requirements.MaxJavaMajor != 0 && p.Runtime.Major > p.Requirements.MaxJavaMajor {
		return FrozenPlan{}, conflict("runtime does not satisfy exact launch requirements")
	}
	if len(p.ContentManualRequired) > 2048 {
		return FrozenPlan{}, invalid("manual content diagnostics exceed bound")
	}
	if p.Pack != nil && (len(p.Pack.Changes) > 8192 || len(p.Pack.Manual) > 8192 || len(p.Pack.Selections) > 8192 || len(p.Pack.Blockers) > 16384) {
		return FrozenPlan{}, invalid("target pack diagnostics exceed bound")
	}
	seen := map[string]bool{}
	for _, artifact := range p.Artifacts {
		filename := artifact.Filename
		if filename == "" || len(filename) > 255 || path.Base(filename) != filename || strings.ContainsAny(filename, "\\:\x00\r\n") || filename == "." || filename == ".." || artifact.Size <= 0 || !(validHex(artifact.SHA256, 64) || artifact.Checksum.Algorithm == "sha1" && validHex(artifact.Checksum.Value, 40) || artifact.Checksum.Algorithm == "sha256" && validHex(artifact.Checksum.Value, 64)) || seen[strings.ToLower(filename)] {
			return FrozenPlan{}, invalid("unsafe or unverified artifact identity")
		}
		seen[strings.ToLower(filename)] = true
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return FrozenPlan{}, err
	}
	canonical := append([]byte(nil), raw...)
	if p.Pack != nil {
		var projection Plan
		_ = json.Unmarshal(raw, &projection)
		projection.Pack.ID = ""
		projection.Pack.Source.Provenance.FetchedAt = time.Time{}
		canonical, err = json.Marshal(projection)
		if err != nil {
			return FrozenPlan{}, err
		}
	}
	sum := sha256.Sum256(canonical)
	return FrozenPlan{raw: raw, digest: hex.EncodeToString(sum[:]), actorID: p.ActorID}, nil
}
func (p FrozenPlan) View() Plan {
	var view Plan
	_ = json.Unmarshal(p.raw, &view)
	view.ActorID = p.actorID
	return view
}
func (p FrozenPlan) Digest() string { return p.digest }

// CheckApply binds confirmation to an unchanged private plan. The transaction
// service must separately recheck current authority, verified compatibility and
// successful safety backup before it acquires activation admission.
func (p FrozenPlan) CheckApply(snapshot Snapshot, actorID int64, now time.Time, confirmed bool) error {
	if len(p.raw) == 0 {
		return conflict("update plan unavailable")
	}
	view := p.View()
	if actorID != p.actorID {
		return domain.E(domain.CodeForbidden, "update plan belongs to another actor")
	}
	if !now.Before(view.ExpiresAt) || snapshot != view.Snapshot {
		return conflict("update preview expired or instance state changed")
	}
	if view.Mode == Manual && !confirmed {
		return invalid("manual update requires explicit confirmation")
	}
	if len(view.Blockers) > 0 {
		return conflict("resolve all update blockers before applying")
	}
	return nil
}
