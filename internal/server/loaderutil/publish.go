package loaderutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
)

// Builder writes only within root on first installation and validates the
// immutable tree on reuse. final is the eventual absolute directory, not cwd.
type Builder func(ctx context.Context, root *os.Root, stage, final string, reuse bool) ([]string, error)
type Publisher struct {
	dataDir    string
	client     *Client
	getRuntime func(context.Context, string) (domain.JavaRuntime, error)
}

func NewPublisher(dataDir string, c *Client) *Publisher {
	return &Publisher{dataDir: dataDir, client: c, getRuntime: managedruntime.New(dataDir, c.http).Get}
}

// generationID binds immutable executable layout to both the pinned loader plan
// and the exact managed Java installation whose identity the receipt retains.
func generationID(plan domain.InstallPlan, r domain.JavaRuntime) string {
	key := struct {
		PlanID       string          `json:"plan_id"`
		RuntimeID    string          `json:"runtime_id"`
		JavaChecksum domain.Checksum `json:"java_checksum"`
	}{providerutil.PlanID(plan), r.ID, r.Artifact.Checksum}
	encoded, _ := json.Marshal(key)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

type fileRecord struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type receipt struct {
	Runtime    domain.JavaRuntime `json:"runtime"`
	Schema     int                `json:"schema"`
	Plan       domain.InstallPlan `json:"plan"`
	Sources    []Source           `json:"sources"`
	Files      []fileRecord       `json:"files"`
	Args       []string           `json:"args"`
	VerifiedAt time.Time          `json:"verified_at"`
}

func WriteFile(root *os.Root, name string, b []byte) error {
	if e := SafePath(name); e != nil {
		return e
	}
	if parent := filepath.ToSlash(filepath.Dir(name)); parent != "." {
		if e := root.MkdirAll(parent, 0700); e != nil {
			return e
		}
	}
	f, e := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	return e
}
func ReadFile(root *os.Root, name string, max int64) ([]byte, error) {
	if e := SafePath(name); e != nil {
		return nil, e
	}
	info, e := root.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, errors.New("invalid installed file")
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, errors.New("installed file exceeds bound")
	}
	return b, nil
}
func DownloadFile(ctx context.Context, c *Client, root *os.Root, name string, a domain.Artifact) error {
	if e := SafePath(name); e != nil {
		return e
	}
	if e := root.MkdirAll(filepath.ToSlash(filepath.Dir(name)), 0700); e != nil {
		return e
	}
	f, e := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	_, e = c.Download(ctx, a, f)
	if e == nil {
		e = ValidateJar(f, a.Size)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	return e
}
func VerifyFile(ctx context.Context, root *os.Root, name string, a domain.Artifact) error {
	b, e := ReadFile(root, name, MaxArtifact)
	if e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if int64(len(b)) != a.Size || Digest(a.Checksum.Algorithm, b) != a.Checksum.Value {
		return errors.New("installed artifact differs from publisher digest")
	}
	return nil
}
func inventory(ctx context.Context, root *os.Root) ([]fileRecord, error) {
	records := []fileRecord{}
	var total int64
	seen := map[string]bool{}
	e := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if name == "." {
			return nil
		}
		if e := SafePath(name); e != nil {
			return e
		}
		key := filepath.ToSlash(name)
		fold := strings.ToLower(key)
		if seen[fold] {
			return errors.New("case-colliding installed paths")
		}
		seen[fold] = true
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("installer produced symlink")
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("installer produced special file")
		}
		if name == "manifest.json" {
			return nil
		}
		if len(records) >= 200000 || info.Size() > MaxArtifact {
			return errors.New("installer output exceeds limits")
		}
		total += info.Size()
		if total > 8<<30 {
			return errors.New("installer output exceeds total limit")
		}
		f, e := root.Open(name)
		if e != nil {
			return e
		}
		h, e := hashLocal(ctx, f)
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return e
		}
		records = append(records, fileRecord{key, info.Size(), h})
		return nil
	})
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	return records, e
}
func hashLocal(ctx context.Context, r io.Reader) (string, error) {
	h := sha256.New()
	if _, e := io.Copy(h, &contextReader{ctx, r}); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func syncDir(root *os.Root, name string) error {
	if goruntime.GOOS == "windows" {
		return nil
	}
	f, e := root.Open(name)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

// Publish never writes a launch pointer, instance state, worlds, configuration,
// mods or EULA. Coordinator admission and activation are separate operations.
func (p *Publisher) Publish(ctx context.Context, id string, plan domain.InstallPlan, r domain.JavaRuntime, sources []Source, w io.Writer, build Builder) (domain.LaunchSpec, error) {
	fail := func(e error) (domain.LaunchSpec, error) { return domain.LaunchSpec{}, e }
	if e := ctx.Err(); e != nil {
		return fail(e)
	}
	if r.Major != plan.Requirements.JavaMajor || r.Major < plan.Requirements.MinJavaMajor || (plan.Requirements.MaxJavaMajor > 0 && r.Major > plan.Requirements.MaxJavaMajor) {
		return fail(errors.New("runtime differs from exact loader requirement"))
	}
	actual, e := p.getRuntime(ctx, r.ID)
	if e != nil {
		return fail(fmt.Errorf("reverify managed Java: %w", e))
	}
	if !reflect.DeepEqual(actual, r) || !filepath.IsAbs(r.Executable) {
		return fail(errors.New("runtime differs from verified managed record"))
	}
	paths, e := instance.NewPaths(p.dataDir)
	if e != nil {
		return fail(e)
	}
	dirs, e := paths.For(id)
	if e != nil {
		return fail(e)
	}
	base, e := paths.Resolve(id, "server", ".manager/loaders")
	if e != nil {
		return fail(e)
	}
	server, e := safepath.OpenRoot(ctx, dirs.Server)
	if e != nil {
		return fail(fmt.Errorf("open provisioned instance: %w", e))
	}
	defer server.Close()
	if e = server.MkdirAll(".manager/loaders", 0700); e != nil {
		return fail(e)
	}
	parent, e := server.OpenRoot(".manager/loaders")
	if e != nil {
		return fail(e)
	}
	defer parent.Close()
	name := generationID(plan, r)
	final := filepath.Join(base, name)
	spec := domain.LaunchSpec{Executable: r.Executable, WorkingDir: dirs.Server, JavaRuntimeID: r.ID, JavaChecksum: r.Artifact.Checksum}
	verify := func() (domain.LaunchSpec, error) {
		info, e := parent.Lstat(name)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fail(errors.New("invalid immutable release directory"))
		}
		root, e := parent.OpenRoot(name)
		if e != nil {
			return fail(e)
		}
		defer root.Close()
		b, e := ReadFile(root, "manifest.json", 16<<20)
		if e != nil {
			return fail(e)
		}
		var m receipt
		if json.Unmarshal(b, &m) != nil || m.Schema != 2 || m.VerifiedAt.IsZero() || !providerutil.SamePlan(m.Plan, plan) || !reflect.DeepEqual(m.Runtime, r) {
			return fail(errors.New("invalid loader publication receipt"))
		}
		files, e := inventory(ctx, root)
		if e != nil {
			return fail(e)
		}
		if !reflect.DeepEqual(files, m.Files) {
			return fail(errors.New("immutable loader files changed"))
		}
		args, e := build(ctx, root, final, final, true)
		if e != nil {
			return fail(e)
		}
		if !reflect.DeepEqual(args, m.Args) {
			return fail(errors.New("loader launch arguments changed"))
		}
		if e := syncPublicationAncestors(parent, server); e != nil {
			return fail(e)
		}
		spec.Args = args
		return spec, nil
	}
	if _, e = parent.Lstat(name); e == nil {
		return verify()
	} else if !os.IsNotExist(e) {
		return fail(e)
	}
	var entropy [16]byte
	if _, e = rand.Read(entropy[:]); e != nil {
		return fail(e)
	}
	stage := ".stage-" + hex.EncodeToString(entropy[:])
	if e = parent.Mkdir(stage, 0700); e != nil {
		return fail(e)
	}
	defer func() { _ = parent.RemoveAll(stage) }()
	root, e := parent.OpenRoot(stage)
	if e != nil {
		return fail(e)
	}
	defer root.Close()
	args, e := build(ctx, root, filepath.Join(base, stage), final, false)
	if e != nil {
		return fail(e)
	}
	files, e := inventory(ctx, root)
	if e != nil {
		return fail(e)
	}
	if len(files) == 0 {
		return fail(errors.New("installer produced no files"))
	}
	m := receipt{Schema: 2, Plan: plan, Runtime: r, Sources: sources, Files: files, Args: args, VerifiedAt: time.Now().UTC()}
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return fail(e)
	}
	if e = WriteFile(root, "manifest.json", b); e != nil {
		return fail(e)
	}
	if e = syncTree(ctx, root); e != nil {
		return fail(e)
	}
	if e = syncDir(root, "."); e != nil {
		return fail(e)
	}
	if e = ctx.Err(); e != nil {
		return fail(e)
	}
	if e = parent.Rename(stage, name); e != nil {
		if _, exists := parent.Lstat(name); exists == nil {
			return verify()
		}
		return fail(e)
	}
	if e = syncPublicationAncestors(parent, server); e != nil {
		return fail(e)
	}
	if w != nil {
		_, _ = io.WriteString(w, "Verified immutable loader release published; lifecycle activation remains pending.\n")
	}
	spec.Args = args
	return spec, nil
}

func syncTree(ctx context.Context, root *os.Root) error {
	dirs := []string{}
	e := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if d.IsDir() {
			dirs = append(dirs, name)
			return nil
		}
		f, e := root.OpenFile(name, os.O_RDWR, 0)
		if e != nil {
			return e
		}
		e = f.Sync()
		ce := f.Close()
		if e == nil {
			e = ce
		}
		return e
	})
	if e != nil {
		return e
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if e = syncDir(root, dir); e != nil {
			return e
		}
	}
	return nil
}

// Sync bottom-up so the generation rename and newly created loaders/.manager
// entries are durable before returning a launch proposal, including reuse races.
func syncPublicationAncestors(parent, server *os.Root) error {
	for _, point := range []struct {
		root *os.Root
		name string
	}{{parent, "."}, {server, ".manager"}, {server, "."}} {
		if e := syncDir(point.root, point.name); e != nil {
			return e
		}
	}
	return nil
}
