package catalog

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type hangarProject struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Namespace   struct {
		Owner string `json:"owner"`
		Slug  string `json:"slug"`
	} `json:"namespace"`
	Supported map[string][]string `json:"supportedPlatforms"`
}
type hangarVersion struct {
	ID        int64               `json:"id"`
	ProjectID int64               `json:"projectId"`
	Name      string              `json:"name"`
	Created   string              `json:"createdAt"`
	Platforms map[string][]string `json:"platformDependencies"`
	Downloads map[string]struct {
		URL         string `json:"downloadUrl"`
		ExternalURL string `json:"externalUrl"`
		Info        *struct {
			Name   string `json:"name"`
			Size   int64  `json:"sizeBytes"`
			SHA256 string `json:"sha256Hash"`
		} `json:"fileInfo"`
	} `json:"downloads"`
	Dependencies map[string][]struct {
		ProjectID   int64  `json:"projectId"`
		Name        string `json:"name"`
		Required    *bool  `json:"required"`
		ExternalURL string `json:"externalUrl"`
		Platform    string `json:"platform"`
	} `json:"pluginDependencies"`
}

func hangarProjectDTO(v hangarProject, t time.Time) (Project, error) {
	if v.ID < 1 || v.Name == "" || !validID(v.Namespace.Owner) || !validID(v.Namespace.Slug) {
		return Project{}, upstream("malformed Hangar project")
	}
	c := blankCompatibility()
	if paper, ok := v.Supported["PAPER"]; ok {
		c.Minecraft = paper
		c.Loaders = []string{"paper"}
	}
	return Project{Provenance: Provenance{Hangar, t}, ID: num(v.ID), Slug: v.Namespace.Slug, Kind: Plugin, Title: v.Name, Description: v.Description, OfficialURL: "https://hangar.papermc.io/" + v.Namespace.Owner + "/" + v.Namespace.Slug, Compatibility: c}, nil
}
func validVersionID(id string) bool {
	return len(id) > 0 && len(id) <= 160 && !strings.ContainsAny(id, "/\\%?#\x00\r\n\t") && !strings.Contains(id, "..")
}
func hangarVersionDTO(v hangarVersion, t time.Time) (Version, error) {
	if v.ID < 1 || v.ProjectID < 1 || !validVersionID(v.Name) || v.Downloads == nil || len(v.Dependencies["PAPER"]) > 1000 {
		return Version{}, upstream("malformed Hangar version")
	}
	c := blankCompatibility()
	c.Evidence = "Hangar PAPER platformDependencies and platform-specific artifact metadata; no tested qualification."
	if paper, ok := v.Platforms["PAPER"]; ok {
		c.Minecraft = paper
		c.Loaders = []string{"paper"}
		c.Environment = "dedicated_server_only"
		c.ServerSide = "supported"
	}
	r := Version{Provenance: Provenance{Hangar, t}, ID: v.Name, UpstreamID: num(v.ID), ProjectID: num(v.ProjectID), Name: v.Name, Number: v.Name, PublishedAt: v.Created, OfficialURL: "https://hangar.papermc.io/" + num(v.ProjectID) + "/versions/" + url.PathEscape(v.Name), Compatibility: c, Files: []File{}, Dependencies: []Dependency{}}
	if f, ok := v.Downloads["PAPER"]; ok {
		file := File{ID: "PAPER", URL: f.URL, Role: "paper_plugin", Primary: true, OfficialURL: r.OfficialURL, Hashes: map[string]string{}}
		if f.Info != nil {
			file.Filename = f.Info.Name
			file.Size = f.Info.Size
			file.Hashes["sha256"] = f.Info.SHA256
		}
		file = normalizeFile(Hangar, file)
		if f.ExternalURL != "" {
			file.URL = ""
			file.ManualReason = "External publisher download requires manual resolution through the official version page."
		}
		r.Files = append(r.Files, file)
	}
	for _, d := range v.Dependencies["PAPER"] {
		if d.Required == nil {
			return Version{}, upstream("missing Hangar dependency requirement")
		}
		rel := "optional"
		if *d.Required {
			rel = "required"
		}
		dep := Dependency{Provider: Hangar, Name: d.Name, Required: *d.Required, Relation: rel, Platform: "paper"}
		if d.ProjectID > 0 {
			dep.ProjectID = num(d.ProjectID)
		}
		if d.ExternalURL != "" || d.ProjectID < 1 {
			dep.ManualURL = safeManualURL(d.ExternalURL)
			if dep.ManualURL == "" {
				dep.ManualURL = r.OfficialURL
			}
		}
		r.Dependencies = append(r.Dependencies, dep)
	}
	return r, nil
}
func bindHangarURL(v *Version, project Project) {
	v.OfficialURL = project.OfficialURL + "/versions/" + url.PathEscape(v.ID)
	for i := range v.Files {
		v.Files[i].OfficialURL = v.OfficialURL
	}
}
func safeManualURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return raw
}
func (s *Service) hangarSearch(ctx context.Context, q Query) (SearchResult, error) {
	v := url.Values{"query": {q.Text}, "limit": {strconv.Itoa(q.Limit)}, "offset": {strconv.Itoa(q.Offset)}, "platform": {"PAPER"}}
	if q.Loader != "" && q.Loader != "paper" {
		return SearchResult{}, invalid("Hangar supports Paper server plugins")
	}
	if q.Minecraft != "" {
		v.Set("version", q.Minecraft)
	}
	var b struct {
		Result     []hangarProject `json:"result"`
		Pagination struct {
			Count int `json:"count"`
		} `json:"pagination"`
	}
	t, e := s.metadata(ctx, Hangar, "/projects", v, &b)
	r := SearchResult{Provenance: Provenance{Hangar, t}, Offset: q.Offset, Limit: q.Limit, Total: b.Pagination.Count, Projects: []Project{}}
	if e != nil {
		return r, e
	}
	if b.Result == nil || len(b.Result) > q.Limit || r.Total < 0 {
		return r, upstream("malformed Hangar search page")
	}
	for _, item := range b.Result {
		p, e := hangarProjectDTO(item, t)
		if e != nil {
			return r, e
		}
		r.Projects = append(r.Projects, p)
	}
	return r, nil
}
func (s *Service) hangarVersions(ctx context.Context, id string, q Query) (VersionPage, error) {
	if q.Loader != "" && q.Loader != "paper" {
		return VersionPage{}, invalid("Hangar supports Paper server plugins")
	}
	project, e := s.Project(ctx, Hangar, id)
	if e != nil {
		return VersionPage{}, e
	}
	values := url.Values{"limit": {strconv.Itoa(q.Limit)}, "offset": {strconv.Itoa(q.Offset)}, "platform": {"PAPER"}, "includeHiddenChannels": {"false"}}
	if q.Minecraft != "" {
		values.Set("platformVersion", q.Minecraft)
	}
	var b struct {
		Result     []hangarVersion `json:"result"`
		Pagination struct {
			Count int `json:"count"`
		} `json:"pagination"`
	}
	t, e := s.metadata(ctx, Hangar, "/projects/"+id+"/versions", values, &b)
	r := VersionPage{Provenance: Provenance{Hangar, t}, Offset: q.Offset, Limit: q.Limit, Total: b.Pagination.Count, Versions: []Version{}}
	if e != nil {
		return r, e
	}
	if b.Result == nil || len(b.Result) > q.Limit || r.Total < 0 {
		return r, upstream("malformed Hangar versions page")
	}
	for _, v := range b.Result {
		d, e := hangarVersionDTO(v, t)
		if e != nil {
			return r, e
		}
		if d.ProjectID != id {
			return r, upstream("Hangar version project mismatch; use exact numeric project ID")
		}
		bindHangarURL(&d, project)
		r.Versions = append(r.Versions, d)
	}
	return r, nil
}
