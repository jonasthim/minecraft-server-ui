// Package mapdata reads bounded saved-world terrain. It never modifies a save,
// executes game code, or exposes arbitrary NBT/player data to map consumers.
package mapdata

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
	"time"
)

type State string

const (
	Ready       State = "ready"
	Missing     State = "missing"
	Stale       State = "stale"
	Unsupported State = "unsupported"
	Corrupt     State = "corrupt"
	Unknown     State = "unknown"
)
const Consistency = "Saved disk data may lag gameplay. Source modification time is containing-file age evidence; other chunks may update that file. Selected chunk save time comes from its region header and depends on the game clock; neither proves live freshness. Identity and content checks detect observed changes; cross-file/cross-tile point-in-time coherence is not guaranteed. Legacy 1.12 numeric mod IDs cannot be resolved without the world's mod registry; unknown blocks retain numeric labels."
const policy = "mapdata-v1-original-palette"

type Discoverer interface {
	Discover(context.Context, string) (world.Discovery, error)
}
type Request struct {
	Dimension string `json:"dimension"`
	ChunkX    int32  `json:"chunk_x"`
	ChunkZ    int32  `json:"chunk_z"`
	MaxY      *int32 `json:"max_y,omitempty"`
}
type Cell struct {
	Y     int32  `json:"y"`
	Block string `json:"block"`
	Color string `json:"color"`
	Known bool   `json:"known"`
	Empty bool   `json:"empty"`
}
type Tile struct {
	State        State      `json:"state"`
	Reason       string     `json:"reason"`
	Dimension    string     `json:"dimension"`
	ChunkX       int32      `json:"chunk_x"`
	ChunkZ       int32      `json:"chunk_z"`
	MaxY         *int32     `json:"max_y,omitempty"`
	Cells        []Cell     `json:"cells"`
	DataVersion  int32      `json:"data_version"`
	Format       string     `json:"format"`
	Codec        string     `json:"codec"`
	SourceMTime  time.Time  `json:"source_mtime"`
	ChunkSavedAt *time.Time `json:"chunk_saved_at,omitempty"`
	SampledAt    time.Time  `json:"sampled_at"`
	Fingerprint  string     `json:"fingerprint"`
	Consistency  string     `json:"consistency"`
}
type Summary struct {
	Discovery   world.Discovery `json:"discovery"`
	State       State           `json:"state"`
	Reason      string          `json:"reason"`
	DataVersion int32           `json:"data_version"`
	VersionName string          `json:"version_name"`
	SourceMTime time.Time       `json:"source_mtime"`
	SampledAt   time.Time       `json:"sampled_at"`
	Consistency string          `json:"consistency"`
}
