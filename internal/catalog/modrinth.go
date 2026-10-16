package catalog

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

type mrProject struct {
	ID           string   `json:"id"`
	ProjectID    string   `json:"project_id"`
	Slug         string   `json:"slug"`
	ProjectType  string   `json:"project_type"`
	AllTypes     []string `json:"all_project_types"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Categories   []string `json:"categories"`
	Loaders      []string `json:"loaders"`
	GameVersions []string `json:"game_versions"`
	Versions     []string `json:"versions"`
	ServerSide   string   `json:"server_side"`
}
type mrVersion struct {
	ID           string   `json:"id"`
	ProjectID    string   `json:"project_id"`
	Name         string   `json:"name"`
	Number       string   `json:"version_number"`
	Published    string   `json:"date_published"`
	Environment  string   `json:"environment"`
	GameVersions []string `json:"game_versions"`
	Loaders      []string `json:"loaders"`
	Files        []struct {
		ID       string            `json:"id"`
		Filename string            `json:"filename"`
		URL      string            `json:"url"`
		Size     int64             `json:"size"`
		Hashes   map[string]string `json:"hashes"`
		Primary  bool              `json:"primary"`
		Type     string            `json:"file_type"`
	} `json:"files"`
	Dependencies []struct {
		ProjectID string `json:"project_id"`
		VersionID string `json:"version_id"`
		Filename  string `json:"file_name"`
		Type      string `json:"dependency_type"`
	} `json:"dependencies"`
}

func blankCompatibility() Compatibility {
	return Compatibility{Minecraft: []string{}, Loaders: []string{}, Environment: "unknown", ServerSide: "unknown", Evidence: "Publisher metadata only; no tested support qualification."}
}
func mrProjectDTO(v mrProject, t time.Time, kind Kind) (Project, error) {
	id := v.ID
	if id == "" {
		id = v.ProjectID
	}
	if !validID(id) || !validID(v.Slug) || v.Title == "" {
		return Project{}, upstream("malformed Modrinth project")
	}
	if kind == "" {
		kind = Kind(v.ProjectType)
		if contains(v.Loaders, "paper") || contains(v.Categories, "paper") {
			kind = Plugin
		}
	}
	if kind != Mod && kind != Plugin && kind != Modpack {
		return Project{}, upstream("unsupported Modrinth project kind")
	}
	c := blankCompatibility()
	c.Minecraft = v.GameVersions
	if c.Minecraft == nil {
		c.Minecraft = v.Versions
	}
	if c.Minecraft == nil {
		c.Minecraft = []string{}
	}
	c.Loaders = v.Loaders
	if c.Loaders == nil {
		c.Loaders = []string{}
	}
	c.Evidence = "Project discovery metadata only; inspect version environment before server selection."
	return Project{Provenance: Provenance{Modrinth, t}, ID: id, Slug: v.Slug, Kind: kind, Title: v.Title, Description: v.Description, OfficialURL: "https://modrinth.com/" + string(kind) + "/" + id, Compatibility: c}, nil
}
func mrVersionDTO(v mrVersion, t time.Time) (Version, error) {
	if !validID(v.ID) || !validID(v.ProjectID) || v.Name == "" || v.Files == nil || len(v.Files) > 100 || len(v.Dependencies) > 1000 {
		return Version{}, upstream("malformed Modrinth version")
	}
	c := blankCompatibility()
	c.Minecraft = v.GameVersions
	c.Loaders = v.Loaders
	if c.Minecraft == nil {
		c.Minecraft = []string{}
	}
	if c.Loaders == nil {
		c.Loaders = []string{}
	}
	if v.Environment != "" {
		c.Environment = v.Environment
	}
	switch v.Environment {
	case "client_only", "singleplayer_only":
		c.ServerSide = "unsupported"
	case "client_only_server_optional", "client_and_server", "server_only", "server_only_client_optional", "dedicated_server_only", "client_or_server", "client_or_server_prefers_both":
		c.ServerSide = "supported"
	}
	r := Version{Provenance: Provenance{Modrinth, t}, ID: v.ID, UpstreamID: v.ID, ProjectID: v.ProjectID, Name: v.Name, Number: v.Number, PublishedAt: v.Published, OfficialURL: "https://modrinth.com/project/" + v.ProjectID + "/version/" + v.ID, Compatibility: c, Files: []File{}, Dependencies: []Dependency{}}
	for _, f := range v.Files {
		id := f.ID
		if id == "" {
			id = f.Filename
		}
		r.Files = append(r.Files, normalizeFile(Modrinth, File{ID: id, Filename: f.Filename, URL: f.URL, Size: f.Size, Hashes: f.Hashes, Primary: f.Primary, Role: f.Type, OfficialURL: r.OfficialURL}))
	}
	for _, d := range v.Dependencies {
		if !contains([]string{"required", "optional", "incompatible", "embedded"}, d.Type) {
			return Version{}, upstream("unknown Modrinth dependency relation")
		}
		if d.ProjectID != "" && !validID(d.ProjectID) || d.VersionID != "" && !validID(d.VersionID) {
			return Version{}, upstream("malformed Modrinth dependency identifier")
		}
		manual := ""
		if d.ProjectID == "" && d.VersionID == "" {
			manual = r.OfficialURL
		}
		r.Dependencies = append(r.Dependencies, Dependency{Provider: Modrinth, ProjectID: d.ProjectID, VersionID: d.VersionID, Name: d.Filename, Relation: d.Type, Required: d.Type == "required", ManualURL: manual})
	}
	return r, nil
}
func (s *Service) mrSearch(ctx context.Context, q Query) (SearchResult, error) {
	facets := [][]string{{"project_type:" + string(q.Kind)}}
	if q.Minecraft != "" {
		facets = append(facets, []string{"versions:" + q.Minecraft})
	}
	if q.Loader != "" {
		facets = append(facets, []string{"categories:" + q.Loader})
	}
	b, _ := json.Marshal(facets)
	values := url.Values{"query": {q.Text}, "facets": {string(b)}, "offset": {strconv.Itoa(q.Offset)}, "limit": {strconv.Itoa(q.Limit)}}
	var v struct {
		Hits  []mrProject `json:"hits"`
		Total int         `json:"total_hits"`
	}
	t, e := s.metadata(ctx, Modrinth, "/search", values, &v)
	r := SearchResult{Provenance: Provenance{Modrinth, t}, Offset: q.Offset, Limit: q.Limit, Total: v.Total, Projects: []Project{}}
	if e != nil {
		return r, e
	}
	if v.Hits == nil || len(v.Hits) > q.Limit || v.Total < 0 {
		return r, upstream("malformed Modrinth search page")
	}
	for _, hit := range v.Hits {
		p, e := mrProjectDTO(hit, t, q.Kind)
		if e != nil {
			return r, e
		}
		r.Projects = append(r.Projects, p)
	}
	return r, nil
}
func (s *Service) mrVersions(ctx context.Context, id string, q Query) (VersionPage, error) {
	values := url.Values{}
	if q.Minecraft != "" {
		b, _ := json.Marshal([]string{q.Minecraft})
		values.Set("game_versions", string(b))
	}
	if q.Loader != "" {
		b, _ := json.Marshal([]string{q.Loader})
		values.Set("loaders", string(b))
	}
	var v []mrVersion
	t, e := s.metadata(ctx, Modrinth, "/project/"+id+"/version", values, &v)
	r := VersionPage{Provenance: Provenance{Modrinth, t}, Offset: q.Offset, Limit: q.Limit, Total: len(v), Versions: []Version{}}
	if e != nil {
		return r, e
	}
	if v == nil || len(v) > 10000 {
		return r, upstream("Modrinth version list exceeds limits or is malformed")
	}
	start := min(q.Offset, len(v))
	end := min(start+q.Limit, len(v))
	for _, item := range v[start:end] {
		d, e := mrVersionDTO(item, t)
		if e != nil {
			return r, e
		}
		if d.ProjectID != id {
			return r, upstream("Modrinth version project mismatch")
		}
		r.Versions = append(r.Versions, d)
	}
	return r, nil
}
