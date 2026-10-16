package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
)

func boundedReadDir(root *os.Root, lim int) ([]fs.DirEntry, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(lim + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > lim {
		return nil, errCorrupt
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// treeDigest includes names, empty directories and raw file hashes. It is used
// only for restore intent reconciliation, never as a filesystem lookup path.
func treeDigest(ctx context.Context, root *os.Root, name string) (string, bool, error) {
	before, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if safePath(name) != nil || !safeInfo(before) {
		return "", false, errCorrupt
	}
	type item struct {
		Name string
		Size int64
		Hash string
		Dir  bool
	}
	items := []item{}
	var total int64
	var count int
	var walk func(*os.Root, string, string, int) error
	walk = func(parent *os.Root, base, relative string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > defaultLimits.entries || depth > defaultLimits.depth || safePath(relative) != nil {
			return errCorrupt
		}
		before, err := parent.Lstat(base)
		if err != nil {
			return err
		}
		if !safeInfo(before) {
			return errCorrupt
		}
		if before.IsDir() {
			child, err := parent.OpenRoot(base)
			if err != nil {
				return err
			}
			defer child.Close()
			after, err := child.Stat(".")
			if err != nil {
				return err
			}
			if !os.SameFile(before, after) {
				return errStale
			}
			entries, err := boundedReadDir(child, defaultLimits.entries-count)
			if err != nil {
				return err
			}
			items = append(items, item{Name: relative, Dir: true})
			for _, v := range entries {
				if err := walk(child, v.Name(), path.Join(relative, v.Name()), depth+1); err != nil {
					return err
				}
			}
			current, err := parent.Lstat(base)
			if err != nil {
				return err
			}
			if !safeInfo(current) || !os.SameFile(before, current) || !before.ModTime().Equal(current.ModTime()) {
				return errStale
			}
			return nil
		}
		if !before.Mode().IsRegular() || before.Size() > defaultLimits.fileBytes || before.Size() > defaultLimits.bytes-total {
			return errCorrupt
		}
		total += before.Size()
		f, err := openRead(parent, base)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, contextReader{ctx, io.LimitReader(f, before.Size()+1)})
		opened, statErr := f.Stat()
		err = errors.Join(readErr, statErr, f.Close())
		if err != nil {
			return err
		}
		current, err := parent.Lstat(base)
		if err != nil {
			return err
		}
		if !safeInfo(current) || !os.SameFile(before, opened) || !os.SameFile(before, current) || n != before.Size() || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
			return errStale
		}
		items = append(items, item{Name: relative, Size: n, Hash: hex.EncodeToString(hash.Sum(nil))})
		return nil
	}
	if err = walk(root, name, name, 0); err != nil {
		return "", false, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	raw, _ := json.Marshal(items)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), true, nil
}

// Sync directory entries bottom-up before a restore intent can outlive manager
// memory. File contents are synced by extraction; directory entries need their
// own durability boundary before the synced blocker authorizes replacement.
func syncTree(ctx context.Context, root *os.Root, depth int) error {
	if depth > defaultLimits.depth {
		return errCorrupt
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := boundedReadDir(root, defaultLimits.entries)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return err
		}
		if !safeInfo(info) {
			return errCorrupt
		}
		if !info.IsDir() {
			continue
		}
		child, err := root.OpenRoot(entry.Name())
		if err != nil {
			return err
		}
		err = syncTree(ctx, child, depth+1)
		closeErr := child.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return err
		}
	}
	return syncDir(root, ".")
}
