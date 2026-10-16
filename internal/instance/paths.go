// Package instance owns instance metadata, configuration and contained paths.
package instance

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type Paths struct{ dataDir string }
type Directories struct{ Root, Server, Logs, Backups, State, RuntimeCache string }

var reserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)

func portablePart(value string) error {
	if value == "" || value == "." || value == ".." || strings.TrimRight(value, " .") != value || strings.ContainsAny(value, "/\\:<>\"|?*\x00") || reserved.MatchString(value) {
		return fmt.Errorf("unsafe portable filename")
	}
	for _, r := range value {
		if r < 32 {
			return fmt.Errorf("control character in filename")
		}
	}
	return nil
}
func NewPaths(dataDir string) (Paths, error) {
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return Paths{}, fmt.Errorf("data directory: %w", err)
	}
	p := Paths{root}
	if err = p.check(root); err != nil {
		return Paths{}, err
	}
	return p, nil
}
func validID(id string) error {
	if err := domain.ValidateInstanceID(id); err != nil {
		return domain.Wrap(domain.CodeValidationFailed, "invalid instance id", err)
	}
	if err := portablePart(id); err != nil {
		return domain.Wrap(domain.CodeValidationFailed, "invalid instance id", err)
	}
	return nil
}

// check rejects symlinks in every existing component, including dataDir parents.
// Operations additionally use os.Root so a concurrent symlink replacement cannot
// escape the opened directory. Returned strings alone are not race-safe handles.
func (p Paths) check(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(absolute, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return fmt.Errorf("inspect path: %w", e)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return domain.E(domain.CodeValidationFailed, "symlinks are not allowed in instance paths")
		}
	}
	return nil
}
func (p Paths) For(id string) (Directories, error) {
	if p.dataDir == "" {
		return Directories{}, domain.E(domain.CodeUnavailable, "instance paths are not configured")
	}
	if err := validID(id); err != nil {
		return Directories{}, err
	}
	root := filepath.Join(p.dataDir, "instances", id)
	d := Directories{Root: root, Server: filepath.Join(root, "server"), Logs: filepath.Join(root, "logs"), Backups: filepath.Join(root, "backups"), State: filepath.Join(root, "state"), RuntimeCache: filepath.Join(p.dataDir, "runtimes")}
	for _, path := range []string{d.Root, d.Server, d.Logs, d.Backups, d.State, d.RuntimeCache} {
		if err := p.check(path); err != nil {
			return Directories{}, err
		}
	}
	return d, nil
}

// Resolve validates portable components and rejects absolute/traversing paths.
func (p Paths) Resolve(id, area, relative string) (string, error) {
	d, err := p.For(id)
	if err != nil {
		return "", err
	}
	base := ""
	switch area {
	case "server":
		base = d.Server
	case "logs":
		base = d.Logs
	case "backups":
		base = d.Backups
	case "state":
		base = d.State
	default:
		return "", domain.E(domain.CodeValidationFailed, "invalid instance path area")
	}
	if relative == "" {
		return base, nil
	}
	if filepath.IsAbs(relative) || strings.Contains(relative, "\\") {
		return "", domain.E(domain.CodeValidationFailed, "invalid relative path")
	}
	for _, part := range strings.Split(relative, "/") {
		if err := portablePart(part); err != nil {
			return "", domain.Wrap(domain.CodeValidationFailed, "invalid relative path", err)
		}
	}
	target := filepath.Join(base, filepath.FromSlash(relative))
	if err = p.check(target); err != nil {
		return "", err
	}
	return target, nil
}
