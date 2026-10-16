package players

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type playerDirectoryLink struct {
	parent *os.Root
	name   string
	info   os.FileInfo
}
type playerServerRoot struct {
	root    *os.Root
	roots   []*os.Root
	anchor  string
	initial os.FileInfo
	links   []playerDirectoryLink
}

func changedPlayerDirectory() error {
	return domain.E(domain.CodeUnavailable, "saved player directory changed or is unsafe; operation may be incomplete")
}

// Anchor at the filesystem root/local Windows drive and retain every opened
// directory. Opening the final absolute path would follow a substituted ancestor
// before its first Lstat. Each child must match both its pre-open identity and
// the parent's current entry before any list bytes are read or written.
func openPlayerServer(absolute string) (*playerServerRoot, error) {
	if !filepath.IsAbs(absolute) {
		return nil, changedPlayerDirectory()
	}
	volume := filepath.VolumeName(absolute)
	// UNC/device/extended volumes require a separately reviewed root policy.
	if volume != "" && (len(volume) != 2 || volume[1] != ':' || !(volume[0] >= 'a' && volume[0] <= 'z' || volume[0] >= 'A' && volume[0] <= 'Z')) {
		return nil, domain.E(domain.CodeUnsupported, "saved player lists require a local filesystem root")
	}
	anchor := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, anchor)
	if relative == absolute {
		return nil, changedPlayerDirectory()
	}
	before, err := os.Lstat(anchor)
	if err != nil {
		return nil, err
	}
	if !safePlayerDirectory(before) {
		return nil, changedPlayerDirectory()
	}
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return nil, err
	}
	pinned := &playerServerRoot{root: root, roots: []*os.Root{root}, anchor: anchor, initial: before}
	after, err := root.Stat(".")
	current, currentErr := os.Lstat(anchor)
	if err != nil || currentErr != nil || !safePlayerDirectory(after) || !safePlayerDirectory(current) || !os.SameFile(before, after) || !os.SameFile(before, current) {
		pinned.Close()
		return nil, changedPlayerDirectory()
	}
	if relative != "" {
		for _, name := range strings.Split(filepath.ToSlash(relative), "/") {
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "\\:\x00") {
				pinned.Close()
				return nil, changedPlayerDirectory()
			}
			before, err := pinned.root.Lstat(name)
			if err != nil {
				pinned.Close()
				return nil, err
			}
			if !safePlayerDirectory(before) {
				pinned.Close()
				return nil, changedPlayerDirectory()
			}
			child, err := pinned.root.OpenRoot(name)
			if err != nil {
				pinned.Close()
				return nil, err
			}
			after, err := child.Stat(".")
			current, currentErr := pinned.root.Lstat(name)
			if err != nil || currentErr != nil || !safePlayerDirectory(after) || !safePlayerDirectory(current) || !os.SameFile(before, after) || !os.SameFile(before, current) {
				child.Close()
				pinned.Close()
				return nil, changedPlayerDirectory()
			}
			pinned.links = append(pinned.links, playerDirectoryLink{parent: pinned.root, name: name, info: before})
			pinned.roots = append(pinned.roots, child)
			pinned.root = child
		}
	}
	if err = pinned.verify(); err != nil {
		pinned.Close()
		return nil, err
	}
	return pinned, nil
}
func (p *playerServerRoot) Close() {
	for i := len(p.roots) - 1; i >= 0; i-- {
		p.roots[i].Close()
	}
}
func (p *playerServerRoot) verify() error {
	current, err := os.Lstat(p.anchor)
	if err != nil || !safePlayerDirectory(current) || !os.SameFile(p.initial, current) {
		return changedPlayerDirectory()
	}
	for _, link := range p.links {
		current, err := link.parent.Lstat(link.name)
		if err != nil || !safePlayerDirectory(current) || !os.SameFile(link.info, current) {
			return changedPlayerDirectory()
		}
	}
	return nil
}
func (p *playerServerRoot) readList(ctx context.Context, kind string) (console.PlayerList, error) {
	if err := ctx.Err(); err != nil {
		return console.PlayerList{}, err
	}
	if err := p.verify(); err != nil {
		return console.PlayerList{}, err
	}
	list, err := savedList(ctx, p.root, kind)
	if err != nil {
		return console.PlayerList{}, err
	}
	if err = p.verify(); err != nil {
		return console.PlayerList{}, err
	}
	return list, nil
}
func (p *playerServerRoot) mutate(ctx context.Context, m console.PlayerMutation, now time.Time) error {
	if err := p.verify(); err != nil {
		return err
	}
	if err := mutateSavedGuarded(ctx, p.root, m, now, p.verify); err != nil {
		return err
	}
	return p.verify()
}
