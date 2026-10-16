package catalog

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type cfProject struct {
	ID      int64  `json:"id"`
	GameID  int    `json:"gameId"`
	ClassID int    `json:"classId"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Summary string `json:"summary"`
}
type cfFile struct {
	ID               int64    `json:"id"`
	ModID            int64    `json:"modId"`
	Name             string   `json:"displayName"`
	Filename         string   `json:"fileName"`
	URL              string   `json:"downloadUrl"`
	Size             int64    `json:"fileLength"`
	Available        bool     `json:"isAvailable"`
	GameVersions     []string `json:"gameVersions"`
	Date             string   `json:"fileDate"`
	ServerPackFileID int64    `json:"serverPackFileId"`
	IsServerPack     bool     `json:"isServerPack"`
	Hashes           []struct {
		Algo  int    `json:"algo"`
		Value string `json:"value"`
	} `json:"hashes"`
	Dependencies []struct {
		ID   int64 `json:"modId"`
		Type int   `json:"relationType"`
	} `json:"dependencies"`
}

func num(n int64) string { return strconv.FormatInt(n, 10) }
func cfProjectDTO(p cfProject, t time.Time) (Project, error) {
	if p.ID < 1 || p.GameID != 432 || p.Name == "" || !validID(p.Slug) || p.ClassID != 6 && p.ClassID != 4471 {
		return Project{}, upstream("malformed CurseForge Minecraft project")
	}
	kind := Mod
	segment := "mc-mods"
	if p.ClassID == 4471 {
		kind = Modpack
		segment = "modpacks"
	}
	return Project{Provenance: Provenance{CurseForge, t}, ID: num(p.ID), Slug: p.Slug, Kind: kind, Title: p.Name, Description: p.Summary, OfficialURL: "https://www.curseforge.com/minecraft/" + segment + "/" + p.Slug, Compatibility: blankCompatibility()}, nil
}
func cfVersionDTO(f cfFile, t time.Time) (Version, error) {
	if f.ID < 1 || f.ModID < 1 || f.Name == "" || len(f.Dependencies) > 1000 {
		return Version{}, upstream("malformed CurseForge file")
	}
	c := blankCompatibility()
	for _, v := range f.GameVersions {
		switch strings.ToLower(v) {
		case "forge", "fabric", "neoforge", "quilt":
			c.Loaders = append(c.Loaders, strings.ToLower(v))
		default:
			if len(v) > 0 && (v[0] >= '0' && v[0] <= '9') {
				c.Minecraft = append(c.Minecraft, v)
			}
		}
	}
	c.Evidence = "CurseForge file gameVersions; server/client side is not declared. Unknown side blocks automatic qualification."
	r := Version{Provenance: Provenance{CurseForge, t}, ID: num(f.ID), UpstreamID: num(f.ID), ProjectID: num(f.ModID), Name: f.Name, Number: f.Name, PublishedAt: f.Date, OfficialURL: "https://www.curseforge.com/projects/" + num(f.ModID) + "/files/" + num(f.ID), Compatibility: c, Files: []File{}, Dependencies: []Dependency{}, IsServerPack: f.IsServerPack}
	if f.ServerPackFileID > 0 {
		r.ServerPackFileID = num(f.ServerPackFileID)
	}
	hashes := map[string]string{}
	for _, h := range f.Hashes {
		if h.Algo == 1 {
			hashes["sha1"] = h.Value
		}
		if h.Algo == 2 {
			hashes["md5"] = h.Value
		}
	}
	role := "artifact"
	if f.IsServerPack {
		role = "server_pack"
	}
	file := normalizeFile(CurseForge, File{ID: num(f.ID), Filename: f.Filename, URL: f.URL, Size: f.Size, Hashes: hashes, Primary: true, Role: role, OfficialURL: r.OfficialURL})
	if !f.Available {
		file.URL = ""
		file.ManualReason = "Publisher marks this file unavailable; resolve through the official page."
	}
	r.Files = append(r.Files, file)
	relations := map[int]string{1: "embedded", 2: "optional", 3: "required", 4: "tool", 5: "incompatible", 6: "include"}
	for _, d := range f.Dependencies {
		if d.ID < 1 {
			return Version{}, upstream("invalid CurseForge dependency")
		}
		rel := relations[d.Type]
		if rel == "" {
			rel = "unknown"
		}
		r.Dependencies = append(r.Dependencies, Dependency{Provider: CurseForge, ProjectID: num(d.ID), Relation: rel, Required: d.Type == 3, ManualURL: ""})
	}
	return r, nil
}
func (s *Service) cfSearch(ctx context.Context, q Query) (SearchResult, error) {
	class := "6"
	if q.Kind == Modpack {
		class = "4471"
	}
	v := url.Values{"gameId": {"432"}, "classId": {class}, "searchFilter": {q.Text}, "index": {strconv.Itoa(q.Offset)}, "pageSize": {strconv.Itoa(q.Limit)}}
	if q.Minecraft != "" {
		v.Set("gameVersion", q.Minecraft)
	}
	if q.Loader != "" {
		n := map[string]string{"forge": "1", "fabric": "4", "quilt": "5", "neoforge": "6"}[q.Loader]
		if n == "" {
			return SearchResult{}, invalid("CurseForge mod loader unsupported")
		}
		v.Set("modLoaderType", n)
	}
	var body struct {
		Data       []cfProject `json:"data"`
		Pagination struct {
			Total int `json:"totalCount"`
		} `json:"pagination"`
	}
	t, e := s.metadata(ctx, CurseForge, "/mods/search", v, &body)
	r := SearchResult{Provenance: Provenance{CurseForge, t}, Offset: q.Offset, Limit: q.Limit, Total: body.Pagination.Total, Projects: []Project{}}
	if e != nil {
		return r, e
	}
	if body.Data == nil || len(body.Data) > q.Limit || r.Total < 0 {
		return r, upstream("malformed CurseForge search page")
	}
	for _, v := range body.Data {
		p, e := cfProjectDTO(v, t)
		if e != nil {
			return r, e
		}
		if p.Kind != q.Kind {
			return r, upstream("CurseForge content kind mismatch")
		}
		r.Projects = append(r.Projects, p)
	}
	return r, nil
}
func (s *Service) cfVersions(ctx context.Context, id string, q Query) (VersionPage, error) {
	if n, e := strconv.ParseInt(id, 10, 32); e != nil || n < 1 {
		return VersionPage{}, invalid("invalid CurseForge project ID")
	}
	values := url.Values{"index": {strconv.Itoa(q.Offset)}, "pageSize": {strconv.Itoa(q.Limit)}}
	if q.Minecraft != "" {
		values.Set("gameVersion", q.Minecraft)
	}
	if q.Loader != "" {
		n := map[string]string{"forge": "1", "fabric": "4", "quilt": "5", "neoforge": "6"}[q.Loader]
		if n == "" {
			return VersionPage{}, invalid("CurseForge mod loader unsupported")
		}
		values.Set("modLoaderType", n)
	}
	var v struct {
		Data       []cfFile `json:"data"`
		Pagination struct {
			Total int `json:"totalCount"`
		} `json:"pagination"`
	}
	t, e := s.metadata(ctx, CurseForge, "/mods/"+id+"/files", values, &v)
	r := VersionPage{Provenance: Provenance{CurseForge, t}, Offset: q.Offset, Limit: q.Limit, Total: v.Pagination.Total, Versions: []Version{}}
	if e != nil {
		return r, e
	}
	if v.Data == nil || len(v.Data) > q.Limit || r.Total < 0 {
		return r, upstream("malformed CurseForge file page")
	}
	for _, f := range v.Data {
		d, e := cfVersionDTO(f, t)
		if e != nil {
			return r, e
		}
		if d.ProjectID != id {
			return r, upstream("CurseForge file project mismatch")
		}
		r.Versions = append(r.Versions, d)
	}
	return r, nil
}
func (s *Service) cfVersion(ctx context.Context, project, id string) (Version, error) {
	for _, v := range []string{project, id} {
		if n, e := strconv.ParseInt(v, 10, 32); e != nil || n < 1 {
			return Version{}, invalid("invalid CurseForge ID")
		}
	}
	var v struct {
		Data cfFile `json:"data"`
	}
	t, e := s.metadata(ctx, CurseForge, "/mods/"+project+"/files/"+id, nil, &v)
	if e != nil {
		return Version{}, e
	}
	r, e := cfVersionDTO(v.Data, t)
	if e == nil && (r.ProjectID != project || r.ID != id) {
		return Version{}, upstream("CurseForge file identity mismatch")
	}
	return r, e
}
