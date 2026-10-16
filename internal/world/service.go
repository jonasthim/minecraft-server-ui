// Package world discovers and captures the configured saved world without changing it.
package world

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

type InstanceStore interface {
	Get(context.Context, string) (instance.Detail, error)
}

// Reservation excludes queued work and lifecycle/file mutations, while allowing
// an already-running JVM. The callback must not call queue methods.
type Reservation interface {
	WithIdle(context.Context, string, func() error) error
}
type Service struct {
	db          *sql.DB
	instances   InstanceStore
	paths       instance.Paths
	reservation Reservation
	limits      limits
}

func New(db *sql.DB, store InstanceStore, paths instance.Paths, reservation Reservation) *Service {
	return &Service{db: db, instances: store, paths: paths, reservation: reservation, limits: defaultLimits}
}

type limits struct {
	entries, depth, dimensions                int
	bytes, fileBytes, archiveBytes, pathBytes int64
	discover, capture                         time.Duration
}

var defaultLimits = limits{100000, 32, 256, 16 << 30, 8 << 30, (16 << 30) + (32 << 20), 8 << 20, 15 * time.Second, 10 * time.Minute}

const Consistency = "Best-effort live copy: files are checked for changes, but this is not a save-lease backup or a consistent point-in-time snapshot. Player and mod data may change while captured."

type Omission struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Dimension struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	State string `json:"state"`
	Kind  string `json:"kind"`
}
type Discovery struct {
	LevelName        string      `json:"level_name"`
	State            string      `json:"state"`
	Layout           string      `json:"layout"`
	MinecraftVersion string      `json:"minecraft_version"`
	Flavor           string      `json:"flavor"`
	Provenance       string      `json:"provenance"`
	Dimensions       []Dimension `json:"dimensions"`
	Warnings         []string    `json:"warnings"`
	Omitted          []Omission  `json:"omitted"`
	Consistency      string      `json:"consistency"`
}

var portableReserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)
var versionPattern = regexp.MustCompile(`^(1|2[6-9]|[3-9][0-9])\.([0-9]+)(?:\.([0-9]+))?$`)
var resourcePart = regexp.MustCompile(`^[a-z0-9_.-]+$`)

func safePart(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.TrimRight(name, " .") != name || strings.ContainsAny(name, "/\\:<>\"|?*\x00") || portableReserved.MatchString(name) {
		return unsafe()
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return unsafe()
		}
	}
	return nil
}
func unsafe() error {
	return domain.E(domain.CodeValidationFailed, "world contains an unsafe path, link, or special file")
}
func changed() error {
	return domain.E(domain.CodeConflict, "world files changed during capture; retry or use a save-lease backup")
}
func bound() error {
	return domain.E(domain.CodeValidationFailed, "world exceeds export count, size, depth, or time bounds")
}
func versionLayout(version string, flavor domain.Flavor) (string, error) {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil {
		return "", domain.E(domain.CodeUnsupported, "unknown world layout version")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	if major == 1 && (minor < 12 || minor == 12 && patch < 2) {
		return "", domain.E(domain.CodeUnsupported, "world layout predates 1.12.2")
	}
	switch flavor {
	case domain.FlavorVanilla, domain.FlavorPaper, domain.FlavorFabric, domain.FlavorForge, domain.FlavorNeoForge:
	default:
		return "", domain.E(domain.CodeUnsupported, "unknown server flavor")
	}
	if major >= 26 {
		return "namespaced_26_1", nil
	}
	if flavor == domain.FlavorPaper {
		return "paper_legacy", nil
	}
	return "legacy", nil
}

// openChild pins the directory identity across Lstat/OpenRoot; os.Root then
// confines every descendant lookup even when an ancestor is renamed concurrently.
func openChild(parent *os.Root, name string) (*os.Root, error) {
	before, e := parent.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !safeDirectoryInfo(before) {
		return nil, unsafe()
	}
	r, e := parent.OpenRoot(name)
	if e != nil {
		return nil, e
	}
	after, e := r.Stat(".")
	current, ce := parent.Lstat(name)
	if e != nil {
		return nil, errors.Join(fmt.Errorf("stat opened world directory: %w", e), r.Close())
	}
	if ce != nil {
		return nil, errors.Join(fmt.Errorf("recheck world directory: %w", ce), r.Close())
	}
	if !os.SameFile(before, after) || !os.SameFile(before, current) || !safeDirectoryInfo(after) || !safeDirectoryInfo(current) {
		return nil, errors.Join(changed(), r.Close())
	}
	return r, nil
}
func (s *Service) openInstance(id string) (*os.Root, error) {
	d, e := s.paths.For(id)
	if e != nil {
		return nil, e
	}
	return openWorldRoot(d.Root)
}

// Bootstrap only from an OS filesystem/local-drive root. Absolute instance
// strings are not handles: following the full path would accept a substituted
// ancestor even when all final-directory identities agree on the outside tree.
func openWorldRoot(absolute string) (_ *os.Root, err error) {
	if !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return nil, unsafe()
	}
	volume := filepath.VolumeName(absolute)
	if volume != "" && (len(volume) != 2 || volume[1] != ':' || !(volume[0] >= 'a' && volume[0] <= 'z' || volume[0] >= 'A' && volume[0] <= 'Z')) {
		return nil, domain.E(domain.CodeUnsupported, "world data requires a local filesystem drive")
	}
	anchor := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, anchor)
	if relative == absolute {
		return nil, unsafe()
	}
	before, e := os.Lstat(anchor)
	if e != nil {
		return nil, fmt.Errorf("inspect world filesystem root: %w", e)
	}
	if !safeDirectoryInfo(before) {
		return nil, unsafe()
	}
	r, e := os.OpenRoot(anchor)
	if e != nil {
		return nil, fmt.Errorf("open world filesystem root: %w", e)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	after, e := r.Stat(".")
	current, ce := os.Lstat(anchor)
	if e != nil {
		return nil, fmt.Errorf("stat opened world filesystem root: %w", e)
	}
	if ce != nil {
		return nil, fmt.Errorf("recheck world filesystem root: %w", ce)
	}
	if !os.SameFile(before, after) || !os.SameFile(before, current) || !safeDirectoryInfo(after) || !safeDirectoryInfo(current) {
		return nil, changed()
	}
	if relative != "" {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			if component == "" || component == "." || component == ".." || strings.ContainsAny(component, "\\:\x00") {
				return nil, unsafe()
			}
			child, openErr := openChild(r, component)
			if openErr != nil {
				return nil, fmt.Errorf("open world root component: %w", openErr)
			}
			closeErr := r.Close()
			r = child
			if closeErr != nil {
				return nil, fmt.Errorf("close world root ancestor: %w", closeErr)
			}
		}
	}
	return r, nil
}

type snapshotEntry struct {
	name    string
	info    fs.FileInfo
	omitted bool
}
type worldRoot struct {
	name    string
	root    *os.Root
	entries []snapshotEntry
}

func closeRoots(roots []worldRoot) {
	for _, r := range roots {
		r.root.Close()
	}
}
func exclusion(name string) string {
	base := strings.ToLower(path.Base(name))
	switch base {
	case "session.lock":
		return "transient game lock"
	case ".manager", "manager.db", "manager.db-wal", "manager.db-shm":
		return "reserved manager data"
	case "server.properties":
		return "server configuration"
	case ".env":
		return "credential environment file"
	}
	if strings.HasPrefix(base, ".env.") {
		return "credential environment file"
	}
	return ""
}
func (s *Service) inventory(ctx context.Context, r *os.Root) ([]snapshotEntry, error) {
	out := []snapshotEntry{}
	seen := map[string]bool{}
	var bytes int64
	visited := 0
	var pathBytes int64
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if depth > s.limits.depth {
			return bound()
		}
		before, e := r.Lstat(dir)
		if e != nil {
			return e
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return unsafe()
		}
		f, e := openRead(r, dir, true)
		if e != nil {
			return e
		}
		opened, se := f.Stat()
		if se != nil || !os.SameFile(before, opened) {
			f.Close()
			return changed()
		}
		entries, e := f.ReadDir(s.limits.entries + 1)
		f.Close()
		if e != nil && e != io.EOF {
			return e
		}
		if len(entries) > s.limits.entries {
			return bound()
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			visited++
			if visited > s.limits.entries {
				return bound()
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			if e := safePart(entry.Name()); e != nil {
				return e
			}
			name := path.Join(dir, entry.Name())
			pathBytes += int64(len(name))
			if pathBytes > s.limits.pathBytes {
				return bound()
			}
			if len(name) > 4096 {
				return bound()
			}
			info, e := r.Lstat(name)
			if e != nil {
				return e
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
				return unsafe()
			}
			if exclusion(name) != "" {
				out = append(out, snapshotEntry{name, info, true})
				continue
			}
			if !info.IsDir() && (strings.HasSuffix(strings.ToLower(name), ".pem") || strings.HasSuffix(strings.ToLower(name), ".key")) {
				return domain.E(domain.CodeValidationFailed, "possible credential file in world; export refused")
			}
			key := strings.ToLower(name)
			if seen[key] {
				return unsafe()
			}
			seen[key] = true
			out = append(out, snapshotEntry{name, info, false})
			if len(out) > s.limits.entries {
				return bound()
			}
			if info.IsDir() {
				if e = walk(name, depth+1); e != nil {
					return e
				}
			} else {
				if info.Size() < 0 || info.Size() > s.limits.fileBytes || bytes > s.limits.bytes-info.Size() {
					return bound()
				}
				bytes += info.Size()
			}
		}
		return nil
	}
	if e := walk(".", 0); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *Service) discover(ctx context.Context, id string, keep bool) (Discovery, []worldRoot, error) {
	d := Discovery{Dimensions: []Dimension{}, Warnings: []string{}, Omitted: []Omission{}, Consistency: Consistency, Provenance: "Configured instance version/flavor plus observed saved directories; save contents and runtime compatibility are not qualified."}
	detail, e := s.instances.Get(ctx, id)
	if e != nil {
		return d, nil, e
	}
	d.LevelName = detail.Config.LevelName
	if exclusion(d.LevelName) != "" {
		return d, nil, unsafe()
	}
	d.MinecraftVersion = detail.Instance.MinecraftVersion
	d.Flavor = string(detail.Instance.Flavor)
	if e = safePart(d.LevelName); e != nil {
		return d, nil, e
	}
	if _, e = s.paths.Resolve(id, "server", d.LevelName); e != nil {
		return d, nil, e
	}
	d.Layout, e = versionLayout(d.MinecraftVersion, detail.Instance.Flavor)
	if e != nil {
		d.State = "unsupported"
		d.Layout = "unknown"
		d.Warnings = append(d.Warnings, "The configured version has no recognized world layout.")
		return d, nil, nil
	}
	inst, e := s.openInstance(id)
	if os.IsNotExist(e) {
		d.State = "absent"
		return d, nil, nil
	}
	if e != nil {
		return d, nil, e
	}
	defer inst.Close()
	server, e := openChild(inst, "server")
	if os.IsNotExist(e) {
		d.State = "absent"
		return d, nil, nil
	}
	if e != nil {
		return d, nil, e
	}
	defer server.Close()
	roots := []worldRoot{}
	success := false
	defer func() {
		if !success || !keep {
			closeRoots(roots)
		}
	}()
	names := []string{d.LevelName}
	if detail.Instance.Flavor == domain.FlavorPaper {
		names = append(names, d.LevelName+"_nether", d.LevelName+"_the_end")
	}
	for _, name := range names {
		if e = safePart(name); e != nil {
			return d, nil, e
		}
		r, err := openChild(server, name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return d, nil, err
		}
		entries, err := s.inventory(ctx, r)
		if err != nil {
			r.Close()
			return d, nil, err
		}
		roots = append(roots, worldRoot{name, r, entries})
	}
	if len(roots) == 0 {
		d.State = "absent"
		success = true
		return d, roots, nil
	}
	mainIndex := -1
	lookup := map[string]fs.FileInfo{}
	total := 0
	var totalBytes int64
	var totalPathBytes int64
	for i, r := range roots {
		if r.name == d.LevelName {
			mainIndex = i
		}
		for _, entry := range r.entries {
			total++
			totalPathBytes += int64(len(r.name) + 1 + len(entry.name))
			if entry.omitted {
				d.Omitted = append(d.Omitted, Omission{path.Join(r.name, entry.name), exclusion(entry.name)})
				continue
			}
			lookup[path.Join(r.name, entry.name)] = entry.info
			if !entry.info.IsDir() {
				totalBytes += entry.info.Size()
			}
		}
	}
	if total > s.limits.entries || totalBytes > s.limits.bytes || totalPathBytes > s.limits.pathBytes {
		return d, nil, bound()
	}
	if mainIndex < 0 {
		d.State = "ambiguous"
		d.Warnings = append(d.Warnings, "Associated dimension saves exist without the configured world.")
		success = true
		return d, roots, nil
	}
	has := func(p string) bool { _, ok := lookup[p]; return ok }
	dir := func(p string) bool { i, ok := lookup[p]; return ok && i.IsDir() }
	level := path.Join(d.LevelName, "level.dat")
	if info, ok := lookup[level]; !ok || !info.Mode().IsRegular() {
		d.State = "ambiguous"
		d.Warnings = append(d.Warnings, "World directory has no regular level.dat save marker.")
	} else {
		d.State = "present"
	}
	old := dir(path.Join(d.LevelName, "region")) || dir(path.Join(d.LevelName, "DIM-1")) || dir(path.Join(d.LevelName, "DIM1")) || len(roots) > 1
	modern := dir(path.Join(d.LevelName, "dimensions/minecraft/overworld")) || dir(path.Join(d.LevelName, "dimensions/minecraft/the_nether")) || dir(path.Join(d.LevelName, "dimensions/minecraft/the_end"))
	if d.Layout == "namespaced_26_1" && old || d.Layout != "namespaced_26_1" && modern {
		d.State = "ambiguous"
		d.Warnings = append(d.Warnings, "Saved standard-dimension layout conflicts with the configured version; export is blocked.")
	}
	standard := []struct{ id, old, modern, sibling string }{{"minecraft:overworld", ".", "overworld", ""}, {"minecraft:the_nether", "DIM-1", "the_nether", "_nether"}, {"minecraft:the_end", "DIM1", "the_end", "_the_end"}}
	for _, spec := range standard {
		p := path.Join(d.LevelName, spec.old)
		if d.Layout == "namespaced_26_1" {
			p = path.Join(d.LevelName, "dimensions/minecraft", spec.modern)
		} else if d.Layout == "paper_legacy" && spec.sibling != "" {
			p = path.Join(d.LevelName+spec.sibling, spec.old)
			if dir(path.Join(d.LevelName, spec.old)) {
				d.State = "ambiguous"
				d.Warnings = append(d.Warnings, "Paper has unmigrated nested standard dimension data.")
			}
		}
		state := "absent"
		if dir(p) || spec.old == "." && d.Layout != "namespaced_26_1" && has(level) {
			state = "present"
		}
		d.Dimensions = append(d.Dimensions, Dimension{spec.id, p, state, "standard"})
	}
	prefix := d.LevelName + "/dimensions/"
	for p, info := range lookup {
		if !info.IsDir() || !strings.HasPrefix(p, prefix) {
			continue
		}
		rel := strings.TrimPrefix(p, prefix)
		parts := strings.Split(rel, "/")
		if len(parts) < 2 {
			continue
		}
		if !resourcePart.MatchString(parts[0]) {
			d.Warnings = append(d.Warnings, "Unrecognized custom dimension namespace retained as opaque world data.")
			continue
		}
		valid := true
		for _, part := range parts[1:] {
			if !resourcePart.MatchString(part) {
				valid = false
			}
		}
		if !valid {
			continue
		}
		id := parts[0] + ":" + strings.Join(parts[1:], "/")
		if id == "minecraft:overworld" || id == "minecraft:the_nether" || id == "minecraft:the_end" {
			continue
		}
		marker := dir(p+"/region") || dir(p+"/entities") || dir(p+"/poi")
		if !marker {
			continue
		}
		d.Dimensions = append(d.Dimensions, Dimension{id, p, "present", "custom"})
	}
	for p, info := range lookup {
		if info.IsDir() && strings.HasPrefix(path.Base(p), "DIM") && path.Dir(p) == d.LevelName && path.Base(p) != "DIM-1" && path.Base(p) != "DIM1" {
			d.Dimensions = append(d.Dimensions, Dimension{"unknown:" + path.Base(p), p, "present", "unknown"})
			d.Warnings = append(d.Warnings, "A mod dimension directory is retained with unknown identity and format.")
		}
	}
	if len(d.Dimensions) > s.limits.dimensions {
		return d, nil, bound()
	}
	sort.Slice(d.Dimensions, func(i, j int) bool { return d.Dimensions[i].ID < d.Dimensions[j].ID })
	d.Warnings = append(d.Warnings, "Omitted transient or reserved paths are listed explicitly. Arbitrary mod/plugin formats and point-in-time consistency are unverified.")
	success = true
	return d, roots, nil
}
func (s *Service) Discover(ctx context.Context, id string) (Discovery, error) {
	if s.instances == nil {
		return Discovery{}, domain.E(domain.CodeUnavailable, "world discovery is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, s.limits.discover)
	defer cancel()
	d, _, e := s.discover(ctx, id, false)
	return d, e
}
