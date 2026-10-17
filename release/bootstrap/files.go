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
	pin  *os.File
}

func (f ownedFile) identity() (os.FileInfo, error) {
	if f.pin == nil {
		return nil, errors.New("created file identity handle is unavailable")
	}
	info, err := f.pin.Stat()
	if err != nil || (f.info != nil && !os.SameFile(info, f.info)) || unsafeInfo(info) || !info.Mode().IsRegular() || !singleFile(f.pin, info) {
		return nil, errors.New("created file identity handle changed")
	}
	return info, nil
}

func (f ownedFile) check(root *os.Root) error {
	identity, err := f.identity()
	if err != nil {
		return err
	}
	now, err := root.Lstat(f.name)
	if err != nil {
		return err
	}
	if unsafeInfo(now) || !now.Mode().IsRegular() || !os.SameFile(now, identity) {
		return errors.New("owned file binding changed")
	}
	return nil
}

type destination struct {
	parent      *checkedRoot
	root        *os.Root
	name        string
	info        os.FileInfo
	files       []ownedFile
	cleanupDone bool
	cleanupErr  error
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
		d.files = append(d.files, ownedFile{name: name, pin: f})
		return nil, err
	}
	pin, err := duplicateCreationFile(f)
	if err != nil {
		// Keep the original creation handle as cleanup authority when duplication
		// itself fails. The operation still fails and never returns the writer.
		d.files = append(d.files, ownedFile{name: name, info: info, pin: f})
		return nil, err
	}
	pinnedInfo, err := pin.Stat()
	if err != nil || !os.SameFile(info, pinnedInfo) || unsafeInfo(pinnedInfo) || !pinnedInfo.Mode().IsRegular() || !singleFile(pin, pinnedInfo) {
		_ = pin.Close()
		d.files = append(d.files, ownedFile{name: name, info: info, pin: f})
		return nil, errors.New("created file identity could not be pinned")
	}
	now, err := d.root.Lstat(name)
	if err != nil || unsafeInfo(now) || !now.Mode().IsRegular() || !os.SameFile(now, pinnedInfo) {
		_ = pin.Close()
		d.files = append(d.files, ownedFile{name: name, info: info, pin: f})
		return nil, errors.New("created file binding changed")
	}
	d.files = append(d.files, ownedFile{name: name, info: pinnedInfo, pin: pin})
	if err = exactMode(f, mode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
func (d *destination) owned(name string) (ownedFile, error) {
	for _, f := range d.files {
		if f.name == name {
			return f, nil
		}
	}
	return ownedFile{}, errors.New("file was not created by this operation")
}

func (d *destination) openOwnedRegular(name string, limit int64) (*os.File, error) {
	owned, err := d.owned(name)
	if err != nil {
		return nil, err
	}
	if err = owned.check(d.root); err != nil {
		return nil, err
	}
	f, err := openRegular(d.root, name, limit)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	identity, identityErr := owned.identity()
	if err != nil || identityErr != nil || !os.SameFile(opened, identity) || owned.check(d.root) != nil {
		f.Close()
		return nil, errors.New("opened file differs from created identity")
	}
	return f, nil
}

func (d *destination) removeOwned(name string) error {
	owned, err := d.owned(name)
	if err != nil {
		return err
	}
	if err = owned.check(d.root); err != nil {
		return err
	}
	return d.root.Remove(name)
}

func (d *destination) cleanup() error {
	if d.cleanupDone {
		return d.cleanupErr
	}
	// Remove only identities created by this operation, through the retained
	// destination handle. A substituted destination pathname is never traversed.
	incomplete := false
	if d.root != nil {
		for i := len(d.files) - 1; i >= 0; i-- {
			f := d.files[i]
			if err := d.removeOwned(f.name); err != nil && !errors.Is(err, os.ErrNotExist) {
				incomplete = true
			}
		}
	}
	// Windows can keep a removed file pending until its last handle closes.
	// Release identity pins after attempting every owned-file removal and
	// before removing the destination directory, while keeping d.root open as
	// the authority for the directory binding.
	if d.closePins() {
		incomplete = true
	}
	if now, e := d.parent.root.Lstat(d.name); e == nil {
		if unsafeInfo(now) || !os.SameFile(now, d.info) || d.parent.root.Remove(d.name) != nil {
			incomplete = true
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		incomplete = true
	}
	d.cleanupDone = true
	if incomplete {
		d.cleanupErr = errors.New("owned output cleanup incomplete")
	}
	return d.cleanupErr
}

func (d *destination) closePins() bool {
	incomplete := false
	for i := range d.files {
		if d.files[i].pin == nil {
			continue
		}
		if err := d.files[i].pin.Close(); err != nil {
			incomplete = true
		}
		d.files[i].pin = nil
	}
	return incomplete
}

func (d *destination) close() {
	_ = d.closePins()
	if d.root != nil {
		_ = d.root.Close()
	}
	d.parent.close()
}
