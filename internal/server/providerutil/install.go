package providerutil

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

type Installer struct {
	dataDir    string
	client     *Client
	getRuntime func(context.Context, string) (domain.JavaRuntime, error)
}

func NewInstaller(dataDir string, client *Client) *Installer {
	return &Installer{dataDir: dataDir, client: client, getRuntime: managedruntime.New(dataDir, client.http).Get}
}

type manifest struct {
	Schema         int                `json:"schema"`
	Plan           domain.InstallPlan `json:"plan"`
	Sources        []Source           `json:"metadata_sources"`
	VerifiedSHA256 string             `json:"verified_sha256"`
	VerifiedAt     time.Time          `json:"verified_at"`
}

var mainClass = regexp.MustCompile(`(?im)^Main-Class:[ \t]*[^\s]+`)

func validateJar(f *os.File, size int64) error {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return errors.New("download is not a valid JAR")
	}
	if len(zr.File) > 200000 {
		return errors.New("JAR has too many entries")
	}
	found := false
	var expanded uint64
	for _, entry := range zr.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || entry.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe JAR entry")
		}
		for _, part := range strings.Split(name, "/") {
			if part == "" || part == "." || part == ".." {
				return errors.New("unsafe JAR entry")
			}
		}
		expanded += entry.UncompressedSize64
		if expanded > 4<<30 || entry.UncompressedSize64 > 1<<30 {
			return errors.New("JAR expanded size exceeds limit")
		}
		if entry.Name != "META-INF/MANIFEST.MF" {
			continue
		}
		if found || entry.UncompressedSize64 > 64<<10 {
			return errors.New("invalid JAR launch manifest")
		}
		reader, e := entry.Open()
		if e != nil {
			return errors.New("read JAR launch manifest")
		}
		data, e := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
		_ = reader.Close()
		if e != nil || len(data) > 64<<10 || !mainClass.Match(data) {
			return errors.New("JAR lacks a valid Main-Class manifest")
		}
		found = true
	}
	if !found {
		return errors.New("JAR lacks a launch manifest")
	}
	return nil
}
func hashFile(ctx context.Context, f *os.File, a domain.Artifact) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	var published hash.Hash = sha256.New()
	if a.Checksum.Algorithm == "sha1" {
		published = sha1.New()
	}
	local := sha256.New()
	buffer := make([]byte, 128<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		read, err := f.Read(buffer)
		if read > 0 {
			n += int64(read)
			_, _ = published.Write(buffer[:read])
			_, _ = local.Write(buffer[:read])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("read installed artifact")
		}
	}
	if n != a.Size || hex.EncodeToString(published.Sum(nil)) != a.Checksum.Value {
		return "", errors.New("artifact checksum or size verification failed")
	}
	return hex.EncodeToString(local.Sum(nil)), nil
}
func verifiedExisting(ctx context.Context, root *os.Root, name string, plan domain.InstallPlan) error {
	for _, relative := range []string{name, name + "/server.jar", name + "/manifest.json"} {
		info, err := root.Lstat(relative)
		if err != nil {
			return fmt.Errorf("inspect installed artifact: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("installed artifact is a symlink")
		}
		if relative != name && !info.Mode().IsRegular() {
			return errors.New("installed artifact is not regular")
		}
	}
	f, err := root.Open(name + "/manifest.json")
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	_ = f.Close()
	if err != nil || len(b) > 1<<20 {
		return errors.New("invalid installation manifest")
	}
	var m manifest
	if json.Unmarshal(b, &m) != nil || m.Schema != 1 || !SamePlan(m.Plan, plan) || m.VerifiedAt.IsZero() || !ValidDigest("sha256", m.VerifiedSHA256) {
		return errors.New("installation manifest differs from pinned plan")
	}
	f, err = root.Open(name + "/server.jar")
	if err != nil {
		return err
	}
	defer f.Close()
	sum, err := hashFile(ctx, f, plan.Artifacts[0])
	if err != nil {
		return err
	}
	if sum != m.VerifiedSHA256 {
		return errors.New("installed JAR integrity mismatch")
	}
	return validateJar(f, plan.Artifacts[0].Size)
}

// Publish accepts an instance ID, never a caller supplied directory. The returned
// LaunchSpec is an activation proposal: only a lifecycle/update coordinator may
// persist it after stopped-state, backup, compatibility and EULA checks.
// No active pointer, supervisor state, world, config or EULA file is written.
func (i *Installer) Publish(ctx context.Context, id string, plan domain.InstallPlan, r domain.JavaRuntime, sources []Source, progress io.Writer) (domain.LaunchSpec, error) {
	fail := func(err error) (domain.LaunchSpec, error) { return domain.LaunchSpec{}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if len(plan.Artifacts) != 1 {
		return fail(errors.New("JAR install requires exactly one pinned artifact"))
	}
	if err := CheckArtifact(i.client.publisher, plan.Artifacts[0]); err != nil {
		return fail(err)
	}
	if plan.Requirements.RequiresInstaller {
		return fail(errors.New("JAR provider cannot execute installers"))
	}
	if r.Major != plan.Requirements.JavaMajor || r.Major < plan.Requirements.MinJavaMajor || (plan.Requirements.MaxJavaMajor > 0 && r.Major > plan.Requirements.MaxJavaMajor) {
		return fail(errors.New("runtime is incompatible with installation plan"))
	}
	verified, err := i.getRuntime(ctx, r.ID)
	if err != nil {
		return fail(fmt.Errorf("verify managed runtime: %w", err))
	}
	if !reflect.DeepEqual(verified, r) || !filepath.IsAbs(r.Executable) {
		return fail(errors.New("supplied runtime differs from managed installation"))
	}
	paths, err := instance.NewPaths(i.dataDir)
	if err != nil {
		return fail(err)
	}
	dirs, err := paths.For(id)
	if err != nil {
		return fail(err)
	}
	// Resolve checks all existing path components for symlinks before opening a root.
	base, err := paths.Resolve(id, "server", ".manager/artifacts")
	if err != nil {
		return fail(err)
	}
	// The instance coordinator must have provisioned the server directory. All
	// creation below it is rooted; a symlink swap cannot escape the server tree.
	serverRoot, err := safepath.OpenRoot(ctx, dirs.Server)
	if err != nil {
		return fail(fmt.Errorf("open provisioned server directory: %w", err))
	}
	defer serverRoot.Close()
	if err = serverRoot.MkdirAll(".manager/artifacts", 0700); err != nil {
		return fail(fmt.Errorf("prepare artifacts directory: %w", err))
	}
	root, err := serverRoot.OpenRoot(".manager/artifacts")
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	name := PlanID(plan)
	spec := domain.LaunchSpec{Executable: r.Executable, Args: []string{"-jar", filepath.Join(base, name, "server.jar"), "nogui"}, WorkingDir: dirs.Server, JavaRuntimeID: r.ID, JavaChecksum: r.Artifact.Checksum}
	if _, err = root.Lstat(name); err == nil {
		if err = verifiedExisting(ctx, root, name, plan); err != nil {
			return fail(err)
		}
		return spec, nil
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	var entropy [16]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return fail(err)
	}
	stage := ".stage-" + hex.EncodeToString(entropy[:])
	if err = root.Mkdir(stage, 0700); err != nil {
		return fail(err)
	}
	defer func() { _ = root.RemoveAll(stage) }()
	f, err := root.OpenFile(stage+"/server.jar", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return fail(err)
	}
	sum, err := i.client.Download(ctx, plan.Artifacts[0], f)
	if err == nil {
		err = validateJar(f, plan.Artifacts[0].Size)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}
	m := manifest{Schema: 1, Plan: plan, Sources: sources, VerifiedSHA256: sum, VerifiedAt: time.Now().UTC()}
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fail(err)
	}
	mf, err := root.OpenFile(stage+"/manifest.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(err)
	}
	_, err = mf.Write(encoded)
	if err == nil {
		err = mf.Sync()
	}
	closeErr = mf.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}
	if err = syncDirectory(root, stage); err != nil {
		return fail(err)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	if err = root.Rename(stage, name); err != nil {
		// A concurrent identical publication is reusable only after full verification.
		if checkErr := verifiedExisting(ctx, root, name, plan); checkErr != nil {
			return fail(fmt.Errorf("publish executable: %w", err))
		}
	}
	for _, point := range []struct {
		root *os.Root
		name string
	}{{root, "."}, {serverRoot, ".manager"}, {serverRoot, "."}} {
		if err = syncDirectory(point.root, point.name); err != nil {
			return fail(err)
		}
	}
	if progress != nil {
		_, _ = io.WriteString(progress, "Verified server artifact published; lifecycle activation is pending.\n")
	}
	return spec, nil
}

// Linux directory fsync makes the publication rename durable. Windows directory
// flushing needs native qualification; file Sync still precedes rename there.
func syncDirectory(root *os.Root, name string) error {
	if goruntime.GOOS == "windows" {
		return nil
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync executable directory: %w", err)
	}
	return nil
}
