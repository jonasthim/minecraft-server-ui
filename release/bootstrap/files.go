package bootstrap

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type binding struct {
	parent *os.Root
	name   string
	info   os.FileInfo
}
type checkedRoot struct {
	root     *os.Root
	roots    []*os.Root
	bindings []binding
}

func absolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n") && localPath(path)
}

// Walk from the volume root, retaining every directory handle and checking each
// opened identity. No path component may be a symlink or Windows reparse point.
func openChecked(path string) (*checkedRoot, error) {
	if !absolutePath(path) {
		return nil, errors.New("invalid absolute path")
	}
	volume := filepath.VolumeName(path)
	base := volume + string(filepath.Separator)
	r, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	c := &checkedRoot{root: r, roots: []*os.Root{r}}
	for _, name := range strings.Split(strings.TrimPrefix(path, base), string(filepath.Separator)) {
		if name == "" {
			continue
		}
		info, e := c.root.Lstat(name)
		if e != nil || !info.IsDir() || unsafeInfo(info) {
			c.close()
			return nil, errors.New("unsafe directory ancestry")
		}
		child, e := c.root.OpenRoot(name)
		if e != nil {
			c.close()
			return nil, e
		}
		opened, e := child.Stat(".")
		if e != nil || !os.SameFile(info, opened) {
			child.Close()
			c.close()
			return nil, errors.New("directory binding changed")
		}
		c.bindings = append(c.bindings, binding{c.root, name, info})
		c.roots = append(c.roots, child)
		c.root = child
	}
	if err = c.check(); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}
func (c *checkedRoot) check() error {
	for _, b := range c.bindings {
		i, e := b.parent.Lstat(b.name)
		if e != nil || unsafeInfo(i) || !os.SameFile(i, b.info) {
			return errors.New("directory binding changed")
		}
	}
	return nil
}
func (c *checkedRoot) close() {
	for i := len(c.roots) - 1; i >= 0; i-- {
		_ = c.roots[i].Close()
	}
}

func openRegular(root *os.Root, name string, limit int64) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil || unsafeInfo(before) || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > limit {
		return nil, errors.New("unsafe or oversized input")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !singleFile(f, after) {
		f.Close()
		return nil, errors.New("input binding changed or is not a single regular file")
	}
	return f, nil
}
func readRegular(ctx context.Context, root *os.Root, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openRegular(root, name, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, limit+1))
	after, e := f.Stat()
	now, lerr := root.Lstat(name)
	if err != nil || e != nil || lerr != nil || !os.SameFile(before, after) || !os.SameFile(after, now) || unsafeInfo(now) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(body)) != after.Size() || int64(len(body)) > limit {
		return nil, errors.New("input changed while reading")
	}
	return body, nil
}

type ownedFile struct {
	name string
	info os.FileInfo
}
type destination struct {
	parent *checkedRoot
	root   *os.Root
	name   string
	info   os.FileInfo
	files  []ownedFile
}

func newDestination(path string) (*destination, error) {
	p, err := openChecked(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	d := &destination{parent: p, name: filepath.Base(path)}
	if _, err = p.root.Lstat(d.name); !errors.Is(err, os.ErrNotExist) {
		p.close()
		return nil, errors.New("output already exists or is inaccessible")
	}
	info, err := privateDirectory(p.root, d.name)
	if err != nil {
		p.close()
		return nil, err
	}
	d.info = info
	d.root, err = p.root.OpenRoot(d.name)
	if err != nil {
		d.cleanup()
		p.close()
		return nil, err
	}
	opened, err := d.root.Stat(".")
	if err != nil || !os.SameFile(opened, info) || d.check() != nil {
		d.cleanup()
		d.root.Close()
		p.close()
		return nil, errors.New("output binding changed")
	}
	return d, nil
}
func (d *destination) check() error {
	if err := d.parent.check(); err != nil {
		return err
	}
	i, e := d.parent.root.Lstat(d.name)
	if e != nil || unsafeInfo(i) || !os.SameFile(d.info, i) {
		return errors.New("output binding changed")
	}
	return nil
}
func (d *destination) create(name string, mode os.FileMode) (*os.File, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	d.files = append(d.files, ownedFile{name, info})
	if err = exactMode(f, mode); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (d *destination) cleanup() {
	// Remove only identities created by this operation, through the retained
	// destination handle. A substituted destination pathname is never traversed.
	if d.root != nil {
		for i := len(d.files) - 1; i >= 0; i-- {
			f := d.files[i]
			now, e := d.root.Lstat(f.name)
			if e == nil && !unsafeInfo(now) && os.SameFile(now, f.info) {
				_ = d.root.Remove(f.name)
			}
		}
	}
	if now, e := d.parent.root.Lstat(d.name); e == nil && !unsafeInfo(now) && os.SameFile(now, d.info) {
		_ = d.parent.root.Remove(d.name)
	}
}
func (d *destination) close() {
	if d.root != nil {
		_ = d.root.Close()
	}
	d.parent.close()
}
