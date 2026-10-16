// Package catalog provides publisher metadata and verified inert artifacts.
// Discovery evidence never qualifies a game/content combination as tested.
package catalog

import "time"

type Provider string

const (
	Modrinth   Provider = "modrinth"
	CurseForge Provider = "curseforge"
	Hangar     Provider = "hangar"
)

type Kind string

const (
	Mod     Kind = "mod"
	Plugin  Kind = "plugin"
	Modpack Kind = "modpack"
)

type Query struct {
	Provider                Provider
	Kind                    Kind
	Text, Minecraft, Loader string
	Offset, Limit           int
}
type Availability struct {
	Provider Provider `json:"provider"`
	Enabled  bool     `json:"enabled"`
	Reason   string   `json:"reason"`
}
type Provenance struct {
	Source    Provider  `json:"source"`
	FetchedAt time.Time `json:"fetched_at"`
}
type Compatibility struct {
	Minecraft   []string `json:"minecraft"`
	Loaders     []string `json:"loaders"`
	Environment string   `json:"environment"`
	ServerSide  string   `json:"server_side"`
	Evidence    string   `json:"evidence"`
	Tested      bool     `json:"tested"`
}
type Project struct {
	Provenance
	ID            string        `json:"id"`
	Slug          string        `json:"slug"`
	Kind          Kind          `json:"kind"`
	Title         string        `json:"title"`
	Description   string        `json:"description"`
	OfficialURL   string        `json:"official_url"`
	Compatibility Compatibility `json:"compatibility"`
}
type SearchResult struct {
	Provenance
	Projects []Project `json:"projects"`
	Offset   int       `json:"offset"`
	Limit    int       `json:"limit"`
	Total    int       `json:"total"`
}
type Dependency struct {
	Provider  Provider `json:"provider"`
	ProjectID string   `json:"project_id"`
	VersionID string   `json:"version_id"`
	Name      string   `json:"name"`
	Relation  string   `json:"relation"`
	Required  bool     `json:"required"`
	ManualURL string   `json:"manual_url"`
	Platform  string   `json:"platform"`
}
type File struct {
	ID           string            `json:"id"`
	Filename     string            `json:"filename"`
	URL          string            `json:"download_url"`
	Size         int64             `json:"size"`
	Hashes       map[string]string `json:"hashes"`
	Primary      bool              `json:"primary"`
	Role         string            `json:"role"`
	ManualReason string            `json:"manual_reason"`
	OfficialURL  string            `json:"official_url"`
}
type Version struct {
	Provenance
	UpstreamID       string        `json:"upstream_id"`
	ID               string        `json:"id"`
	ProjectID        string        `json:"project_id"`
	Name             string        `json:"name"`
	Number           string        `json:"number"`
	PublishedAt      string        `json:"published_at"`
	OfficialURL      string        `json:"official_url"`
	Compatibility    Compatibility `json:"compatibility"`
	Dependencies     []Dependency  `json:"dependencies"`
	Files            []File        `json:"files"`
	ServerPackFileID string        `json:"server_pack_file_id"`
	IsServerPack     bool          `json:"is_server_pack"`
}
type VersionPage struct {
	Provenance
	Versions []Version `json:"versions"`
	Offset   int       `json:"offset"`
	Limit    int       `json:"limit"`
	Total    int       `json:"total"`
}
type Selection struct {
	Provider  Provider `json:"provider"`
	ProjectID string   `json:"project_id"`
	VersionID string   `json:"version_id"`
	FileID    string   `json:"file_id"`
}
type VerifiedArtifact struct {
	Selection
	Provenance
	File          File          `json:"file"`
	Compatibility Compatibility `json:"compatibility"`
	SHA256        string        `json:"sha256"`
	Path          string        `json:"-"`
}
