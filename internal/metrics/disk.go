package metrics

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path"
)

// disk measures logical bytes only within this instance. Shared runtimes and
// manager.db are outside the opened root. ReadDir batches bound allocation even
// when one directory contains millions of entries. Partial scans are unavailable.
func (s *Service) disk(ctx context.Context, id string) (int64, error) {
	paths, e := s.cfg.Paths.For(id)
	if e != nil {
		return 0, e
	}
	root, e := os.OpenRoot(paths.Root)
	if e != nil {
		return 0, e
	}
	defer root.Close()
	var total int64
	entries := 0
	var walk func(string, int) error
	walk = func(name string, depth int) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if depth > 32 {
			return fmt.Errorf("disk depth bound")
		}
		directory, e := root.Open(name)
		if e != nil {
			return e
		}
		defer directory.Close()
		info, e := directory.Stat()
		if e != nil || !info.IsDir() {
			return fmt.Errorf("disk directory changed")
		}
		for {
			batch, e := directory.ReadDir(128)
			if e != nil && e != io.EOF {
				return e
			}
			for _, entry := range batch {
				if e := ctx.Err(); e != nil {
					return e
				}
				entries++
				if entries > 20000 {
					return fmt.Errorf("disk entry bound")
				}
				child := path.Join(name, entry.Name())
				info, e := root.Lstat(child)
				if e != nil {
					return e
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("disk symlink rejected")
				}
				if info.IsDir() {
					if e = walk(child, depth+1); e != nil {
						return e
					}
					continue
				}
				if !info.Mode().IsRegular() || info.Size() < 0 || total > math.MaxInt64-info.Size() {
					return fmt.Errorf("disk entry unavailable")
				}
				total += info.Size()
			}
			if e == io.EOF {
				return nil
			}
		}
	}
	e = walk(".", 0)
	return total, e
}
