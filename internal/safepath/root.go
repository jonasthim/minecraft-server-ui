// Package safepath opens checked local directory roots without following an
// unchecked absolute path through replaceable ancestors.
package safepath

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func unsafePath() error {
	return domain.E(domain.CodeValidationFailed, "directory path is not a canonical safe local directory")
}
func changedPath() error { return domain.E(domain.CodeUnavailable, "directory changed while opening") }

// OpenRoot requires an absolute, clean path. It anchors at the filesystem root
// (or a local Windows drive), checks each component without following symlinks
// or reparse points, and pins the returned directory. The handle does not imply
// that the original absolute path continues to name that directory afterwards;
// callers needing that guarantee must retain and recheck their own chain.
// Errors preserve underlying host and cleanup causes through errors.Is/As.
func OpenRoot(ctx context.Context, absoluteCanonicalPath string) (_ *os.Root, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	absolute := absoluteCanonicalPath
	if !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return nil, unsafePath()
	}
	volume := filepath.VolumeName(absolute)
	if volume != "" && (len(volume) != 2 || volume[1] != ':' || !(volume[0] >= 'a' && volume[0] <= 'z' || volume[0] >= 'A' && volume[0] <= 'Z')) {
		return nil, domain.E(domain.CodeUnsupported, "directory requires a local filesystem drive")
	}
	anchor := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, anchor)
	if relative == absolute {
		return nil, unsafePath()
	}
	before, e := os.Lstat(anchor)
	if e != nil {
		return nil, fmt.Errorf("inspect filesystem root: %w", e)
	}
	if !safeDirectory(before) {
		return nil, unsafePath()
	}
	root, e := os.OpenRoot(anchor)
	if e != nil {
		return nil, fmt.Errorf("open filesystem root: %w", e)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, root.Close())
		}
	}()
	after, e := root.Stat(".")
	current, ce := os.Lstat(anchor)
	if e != nil {
		return nil, fmt.Errorf("stat opened filesystem root: %w", e)
	}
	if ce != nil {
		return nil, fmt.Errorf("recheck filesystem root: %w", ce)
	}
	if !sameDirectory(before, after, current) {
		return nil, changedPath()
	}
	if relative != "" {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			if component == "" || component == "." || component == ".." || !filepath.IsLocal(component) || strings.ContainsRune(component, 0) {
				return nil, unsafePath()
			}
			child, e := openChild(ctx, root, component)
			if e != nil {
				return nil, fmt.Errorf("open directory component: %w", e)
			}
			closeErr := root.Close()
			root = child
			if closeErr != nil {
				return nil, fmt.Errorf("close directory ancestor: %w", closeErr)
			}
		}
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return root, nil
}

func sameDirectory(before, opened, current os.FileInfo) bool {
	return safeDirectory(opened) && safeDirectory(current) && os.SameFile(before, opened) && os.SameFile(before, current)
}

func openChild(ctx context.Context, parent *os.Root, name string) (_ *os.Root, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	before, e := parent.Lstat(name)
	if e != nil {
		return nil, fmt.Errorf("inspect directory: %w", e)
	}
	if !safeDirectory(before) {
		return nil, unsafePath()
	}
	child, e := parent.OpenRoot(name)
	if e != nil {
		return nil, fmt.Errorf("open directory: %w", e)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, child.Close())
		}
	}()
	after, e := child.Stat(".")
	current, ce := parent.Lstat(name)
	if e != nil {
		return nil, fmt.Errorf("stat opened directory: %w", e)
	}
	if ce != nil {
		return nil, fmt.Errorf("recheck directory: %w", ce)
	}
	if !sameDirectory(before, after, current) {
		return nil, changedPath()
	}
	return child, nil
}
