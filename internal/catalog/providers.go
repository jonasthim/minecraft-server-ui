package catalog

import (
	"context"
	"net/url"
	"strconv"
)

func (s *Service) Search(ctx context.Context, q Query) (SearchResult, error) {
	q, e := normalize(q)
	if e != nil {
		return SearchResult{}, e
	}
	switch q.Provider {
	case Modrinth:
		return s.mrSearch(ctx, q)
	case CurseForge:
		return s.cfSearch(ctx, q)
	case Hangar:
		return s.hangarSearch(ctx, q)
	}
	return SearchResult{}, invalid("unknown provider")
}
func (s *Service) Project(ctx context.Context, p Provider, id string) (Project, error) {
	if !validProvider(p) || !validID(id) {
		return Project{}, invalid("invalid catalog project identifier")
	}
	switch p {
	case Modrinth:
		var v mrProject
		t, e := s.metadata(ctx, p, "/project/"+id, nil, &v)
		if e != nil {
			return Project{}, e
		}
		return mrProjectDTO(v, t, "")
	case CurseForge:
		if n, e := strconv.ParseInt(id, 10, 32); e != nil || n < 1 {
			return Project{}, invalid("CurseForge ID must be a positive integer")
		}
		var v struct {
			Data cfProject `json:"data"`
		}
		t, e := s.metadata(ctx, p, "/mods/"+id, nil, &v)
		if e != nil {
			return Project{}, e
		}
		d, e := cfProjectDTO(v.Data, t)
		if e == nil && d.ID != id {
			return Project{}, upstream("CurseForge project identity mismatch")
		}
		return d, e
	case Hangar:
		var v hangarProject
		t, e := s.metadata(ctx, p, "/projects/"+id, nil, &v)
		if e != nil {
			return Project{}, e
		}
		return hangarProjectDTO(v, t)
	}
	return Project{}, invalid("unknown provider")
}
func (s *Service) Versions(ctx context.Context, p Provider, id string, q Query) (VersionPage, error) {
	if !validProvider(p) || !validID(id) {
		return VersionPage{}, invalid("invalid catalog project identifier")
	}
	q.Provider = p
	if p == Hangar {
		q.Kind = Plugin
	}
	q, e := normalize(q)
	if e != nil {
		return VersionPage{}, e
	}
	switch p {
	case Modrinth:
		return s.mrVersions(ctx, id, q)
	case CurseForge:
		return s.cfVersions(ctx, id, q)
	case Hangar:
		return s.hangarVersions(ctx, id, q)
	}
	return VersionPage{}, invalid("unknown provider")
}
func (s *Service) Version(ctx context.Context, p Provider, project, id string) (Version, error) {
	if !validProvider(p) || !validID(project) || !(validID(id) || p == Hangar && validVersionID(id)) {
		return Version{}, invalid("invalid catalog version identifier")
	}
	switch p {
	case Modrinth:
		var v mrVersion
		t, e := s.metadata(ctx, p, "/version/"+id, nil, &v)
		if e != nil {
			return Version{}, e
		}
		d, e := mrVersionDTO(v, t)
		if e == nil && (d.ProjectID != project || d.ID != id) {
			return Version{}, upstream("catalog project/version mismatch")
		}
		return d, e
	case CurseForge:
		return s.cfVersion(ctx, project, id)
	case Hangar:
		pr, e := s.Project(ctx, p, project)
		if e != nil {
			return Version{}, e
		}
		var v hangarVersion
		t, e := s.metadata(ctx, p, "/projects/"+project+"/versions/"+url.PathEscape(id), nil, &v)
		if e != nil {
			return Version{}, e
		}
		d, e := hangarVersionDTO(v, t)
		if e == nil && (d.ID != id || d.ProjectID != project) {
			return Version{}, upstream("catalog project/version mismatch; use exact numeric project ID")
		}
		if e == nil {
			bindHangarURL(&d, pr)
		}
		return d, e
	}
	return Version{}, invalid("unknown provider")
}
