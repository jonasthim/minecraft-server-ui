package content

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"os"
	"sort"
)

type ConfigListing struct {
	Files       []ConfigFile `json:"files"`
	Truncated   bool         `json:"truncated"`
	Unsupported int          `json:"unsupported"`
}

func (s *Service) ConfigList(ctx context.Context, id string) (ConfigListing, error) {
	result := ConfigListing{Files: []ConfigFile{}}
	if _, e := s.instances.Get(ctx, id); e != nil {
		return result, e
	}
	root, e := s.openServer(ctx, id)
	if e != nil {
		return result, e
	}
	defer root.Close()
	visited := 0
	var walk func(*os.Root, string, int) error
	walk = func(parentRoot *os.Root, prefix string, depth int) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if depth > 8 {
			result.Unsupported++
			return nil
		}
		directory, e := parentRoot.Open(".")
		if e != nil {
			return e
		}
		defer directory.Close()
		for {
			entries, e := directory.ReadDir(64)
			for _, entry := range entries {
				visited++
				if visited > 1000 {
					result.Truncated = true
					return nil
				}
				name := prefix + "/" + entry.Name()
				if !portable(entry.Name()) || blockedConfigPart(entry.Name()) {
					result.Unsupported++
					continue
				}
				fi, e := parentRoot.Lstat(entry.Name())
				if e != nil {
					return e
				}
				if fi.IsDir() {
					child, e := parentRoot.OpenRoot(entry.Name())
					if e != nil {
						return e
					}
					opened, e := child.Stat(".")
					if e != nil || !os.SameFile(fi, opened) {
						child.Close()
						return conflict("configuration directory identity changed")
					}
					e = walk(child, name, depth+1)
					child.Close()
					if e != nil {
						return e
					}
					if result.Truncated {
						return nil
					}
					continue
				}
				if !configPath(name) || !fi.Mode().IsRegular() || fi.Size() > MaxConfigBytes {
					result.Unsupported++
					continue
				}
				f, e := openChecked(root, name, MaxConfigBytes)
				if e != nil {
					result.Unsupported++
					continue
				}
				f.Close()
				result.Files = append(result.Files, ConfigFile{name, fi.Size()})
			}
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return e
			}
		}
	}
	for _, area := range []string{"config", "defaultconfigs", "plugins"} {
		fi, e := root.Lstat(area)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return result, e
		}
		if !fi.IsDir() {
			result.Unsupported++
			continue
		}
		child, e := root.OpenRoot(area)
		if e != nil {
			return result, e
		}
		opened, e := child.Stat(".")
		if e != nil || !os.SameFile(fi, opened) {
			child.Close()
			return result, conflict("configuration root changed")
		}
		e = walk(child, area, 1)
		child.Close()
		if e != nil {
			return result, e
		}
		if result.Truncated {
			break
		}
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}
func (s *Service) ConfigRead(ctx context.Context, id, name string, actor domain.User) (ConfigText, error) {
	if _, e := s.actor(ctx, actor.ID, false); e != nil {
		return ConfigText{}, e
	}
	if !configPath(name) {
		return ConfigText{}, invalid("configuration path is outside the editor allowlist")
	}
	if _, e := s.instances.Get(ctx, id); e != nil {
		return ConfigText{}, e
	}
	root, e := s.openServer(ctx, id)
	if e != nil {
		return ConfigText{}, e
	}
	defer root.Close()
	b, e := readFile(ctx, root, name, MaxConfigBytes)
	if os.IsNotExist(e) {
		return ConfigText{}, domain.NotFound("configuration file")
	}
	if e != nil {
		return ConfigText{}, e
	}
	if !validText(b) {
		return ConfigText{}, invalid("configuration is not bounded UTF-8 text")
	}
	return ConfigText{name, digest(b), string(b)}, nil
}
