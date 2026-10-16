// Package runtime manages private, verified Eclipse Temurin installations.
// It never changes the host's JAVA_HOME, PATH, package manager or registry.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

const metadataEndpoint = "https://api.adoptium.net/v3/assets/latest/"

var runtimeIDPattern = regexp.MustCompile(`^temurin-(8|11|16|17|21|25)-(linux|windows)-x64-[a-f0-9]{64}$`)
var publisherRepoPattern = regexp.MustCompile(`^/adoptium/temurin(8|11|16|17|21|25)-binaries/releases/download/[^/]+/[^/]+$`)
var versionPattern = regexp.MustCompile(`^[0-9][A-Za-z0-9.+_\-]{0,99}$`)
var installLocks sync.Map

// Manager is safe for concurrent callers, including multiple managers sharing
// the same data directory inside the application process. Keep runtimes while
// instances can reference them; deletion needs the instance reference graph.
type Manager struct {
	dataDir string
	client  *http.Client
	probe   func(context.Context, string) (jvmInfo, error)
}

func hostOS() string { return goruntime.GOOS }
func New(dataDir string, client *http.Client) *Manager {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Minute}
	}
	c := *client
	if c.Timeout == 0 {
		c.Timeout = 20 * time.Minute
	}
	prior := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return fmt.Errorf("too many runtime download redirects")
		}
		if err := allowedURL(req.URL.String(), true); err != nil {
			return err
		}
		if prior != nil {
			return prior(req, via)
		}
		return nil
	}
	// A caller's shared cookie jar must not inject account credentials into the
	// public metadata/artifact requests.
	c.Jar = nil
	abs, err := filepath.Abs(dataDir)
	if err == nil {
		dataDir = abs
	}
	return &Manager{dataDir: dataDir, client: &c, probe: probeJava}
}
func supportedMajor(major int) bool {
	switch major {
	case 8, 11, 16, 17, 21, 25:
		return true
	}
	return false
}
func supportedHost() error {
	if goruntime.GOARCH != "amd64" || (hostOS() != "linux" && hostOS() != "windows") {
		return fmt.Errorf("managed Temurin requires Linux or Windows x64")
	}
	return nil
}
func (m *Manager) root() string { return filepath.Join(m.dataDir, "runtimes") }
func (m *Manager) checkRoot() error {
	info, err := os.Lstat(m.root())
	if err != nil {
		return fmt.Errorf("inspect runtime root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime root is not a real directory")
	}
	return nil
}

func (m *Manager) prepare() error {
	if err := os.MkdirAll(m.root(), 0700); err != nil {
		return fmt.Errorf("create runtime root: %w", err)
	}
	info, err := os.Lstat(m.root())
	if err != nil {
		return fmt.Errorf("stat runtime root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime root is not a real directory")
	}
	return nil
}
func lock(ctx context.Context, key string) (func(), error) {
	v, _ := installLocks.LoadOrStore(key, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Ensure returns an installed, integrity-checked runtime for this major. Existing
// installations are reused without fetching "latest"; callers persist ID to pin
// the exact version for an instance. Interrupted transfers retry from byte zero.
func (m *Manager) Ensure(ctx context.Context, major int, progress io.Writer) (domain.JavaRuntime, error) {
	if err := ctx.Err(); err != nil {
		return domain.JavaRuntime{}, err
	}
	if err := supportedHost(); err != nil {
		return domain.JavaRuntime{}, err
	}
	if !supportedMajor(major) {
		return domain.JavaRuntime{}, fmt.Errorf("unsupported managed Java major %d", major)
	}
	release, err := lock(ctx, m.root())
	if err != nil {
		return domain.JavaRuntime{}, err
	}
	defer release()
	if err := m.prepare(); err != nil {
		return domain.JavaRuntime{}, err
	}
	installed, err := m.List(ctx)
	if err != nil {
		return domain.JavaRuntime{}, err
	}
	for _, r := range installed {
		if r.Major == major && r.OS == hostOS() {
			if err := m.verifyProbe(ctx, r); err != nil {
				return domain.JavaRuntime{}, err
			}
			return r, nil
		}
	}
	meta, err := m.resolve(ctx, major)
	if err != nil {
		return domain.JavaRuntime{}, err
	}
	id := runtimeID(meta)
	dest := filepath.Join(m.root(), id)
	if _, err := os.Lstat(dest); err == nil {
		return m.Get(ctx, id)
	} else if !os.IsNotExist(err) {
		return domain.JavaRuntime{}, fmt.Errorf("check runtime destination: %w", err)
	}
	stage, err := os.MkdirTemp(m.root(), ".stage-")
	if err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("create runtime staging: %w", err)
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "archive")
	digest, err := m.download(ctx, meta, archive, progress)
	if err != nil {
		return domain.JavaRuntime{}, err
	}
	extracted := filepath.Join(stage, "tree")
	if err := os.Mkdir(extracted, 0700); err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("create extraction staging: %w", err)
	}
	format := "tar.gz"
	if hostOS() == "windows" {
		format = "zip"
	}
	if err := extractArchive(ctx, archive, format, extracted); err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("extract Temurin: %w", err)
	}
	roots, err := os.ReadDir(extracted)
	if err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("read extracted layout: %w", err)
	}
	if len(roots) != 1 || !roots[0].IsDir() {
		return domain.JavaRuntime{}, fmt.Errorf("Temurin archive must contain one runtime directory")
	}
	tree := filepath.Join(extracted, roots[0].Name())
	bin := filepath.Join("bin", "java")
	if hostOS() == "windows" {
		bin += ".exe"
	}
	exe := filepath.Join(tree, bin)
	info, err := os.Lstat(exe)
	if err != nil || !info.Mode().IsRegular() {
		return domain.JavaRuntime{}, fmt.Errorf("Temurin archive has no regular Java executable")
	}
	r := domain.JavaRuntime{ID: id, Vendor: "Eclipse Adoptium", Major: major, Version: meta.Version.OpenJDKVersion, OS: hostOS(), Architecture: "x64", Executable: exe, Artifact: domain.Artifact{ID: hex.EncodeToString(digest), Provider: "adoptium", VersionID: meta.ReleaseName, Filename: meta.Binary.Package.Name, SourceURL: meta.Binary.Package.Link, Size: meta.Binary.Package.Size, Checksum: domain.Checksum{Algorithm: "sha256", Value: hex.EncodeToString(digest)}, RetrievedAt: time.Now().UTC()}}
	if err := validateRelease(tree, r); err != nil {
		return domain.JavaRuntime{}, err
	}
	if err := m.verifyProbe(ctx, r); err != nil {
		return domain.JavaRuntime{}, err
	}
	files, err := hashTree(ctx, tree)
	if err != nil {
		return domain.JavaRuntime{}, err
	}
	r.Executable = filepath.Join(dest, bin)
	record := manifest{Schema: 1, Runtime: r, Files: files, VerifiedSHA256: hex.EncodeToString(digest), VerifiedAt: time.Now().UTC()}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("encode runtime manifest: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(tree, ".runtime.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("create runtime manifest: %w", err)
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("persist runtime manifest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return domain.JavaRuntime{}, err
	}
	if err := os.Rename(tree, dest); err != nil {
		// An independent process may have installed this same immutable artifact.
		// Never overwrite any published runtime; accept only the valid exact winner.
		if existing, getErr := m.Get(ctx, id); getErr == nil {
			return existing, nil
		}
		return domain.JavaRuntime{}, fmt.Errorf("activate runtime: %w", err)
	}
	return r, nil
}
func (m *Manager) EnsureRequirements(ctx context.Context, r domain.LaunchRequirements, w io.Writer) (domain.JavaRuntime, error) {
	if r.JavaMajor < r.MinJavaMajor || (r.MaxJavaMajor != 0 && r.JavaMajor > r.MaxJavaMajor) {
		return domain.JavaRuntime{}, fmt.Errorf("preferred Java major violates launch requirements")
	}
	return m.Ensure(ctx, r.JavaMajor, w)
}

type metadata struct {
	Vendor      string `json:"vendor"`
	ReleaseName string `json:"release_name"`
	Version     struct {
		Major          int    `json:"major"`
		OpenJDKVersion string `json:"openjdk_version"`
	} `json:"version"`
	Binary struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		ImageType    string `json:"image_type"`
		JVMImpl      string `json:"jvm_impl"`
		HeapSize     string `json:"heap_size"`
		Package      struct {
			Name     string `json:"name"`
			Link     string `json:"link"`
			Checksum string `json:"checksum"`
			Size     int64  `json:"size"`
		} `json:"package"`
	} `json:"binary"`
}

func runtimeID(m metadata) string {
	return fmt.Sprintf("temurin-%d-%s-x64-%s", m.Version.Major, m.Binary.OS, strings.ToLower(m.Binary.Package.Checksum))
}
func allowedURL(raw string, redirect bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid publisher URL")
	}
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || strings.ContainsAny(u.Path, "\\\x00") || strings.Contains(u.Path, "/../") || strings.Contains(u.Path, "/./") {
		return fmt.Errorf("unsafe publisher URL")
	}
	switch u.Hostname() {
	case "api.adoptium.net":
		if strings.HasPrefix(u.Path, "/v3/") {
			return nil
		}
	case "github.com":
		if publisherRepoPattern.MatchString(u.Path) && u.RawQuery == "" {
			return nil
		}
	case "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		if redirect {
			return nil
		}
	}
	return fmt.Errorf("URL is outside official Temurin publishers")
}
func (m *Manager) request(ctx context.Context, raw string) (*http.Response, error) {
	if err := allowedURL(raw, false); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("create runtime request: %w", err)
	}
	req.Header.Set("User-Agent", "Minecraft-Server-UI/Temurin")
	resp, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("official runtime request failed")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("official runtime request returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func (m *Manager) resolve(ctx context.Context, major int) (metadata, error) {
	raw := metadataEndpoint + strconv.Itoa(major) + "/hotspot?architecture=x64&heap_size=normal&image_type=jdk&os=" + hostOS() + "&vendor=eclipse"
	resp, err := m.request(ctx, raw)
	if err != nil {
		return metadata{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(&contextReader{ctx: ctx, r: io.LimitReader(resp.Body, (2<<20)+1)})
	if err != nil {
		return metadata{}, fmt.Errorf("read Adoptium metadata: %w", err)
	}
	if len(body) > 2<<20 {
		return metadata{}, fmt.Errorf("Adoptium metadata too large")
	}
	var entries []metadata
	if err := json.Unmarshal(body, &entries); err != nil {
		return metadata{}, fmt.Errorf("decode Adoptium metadata: %w", err)
	}
	if len(entries) != 1 {
		return metadata{}, fmt.Errorf("expected one official Temurin runtime artifact")
	}
	v := entries[0]
	p := v.Binary.Package
	if v.Vendor != "eclipse" || v.Version.Major != major || v.Binary.OS != hostOS() || v.Binary.Architecture != "x64" || v.Binary.ImageType != "jdk" || v.Binary.JVMImpl != "hotspot" || v.Binary.HeapSize != "normal" || !versionPattern.MatchString(v.Version.OpenJDKVersion) || v.ReleaseName == "" || len(v.ReleaseName) > 150 {
		return metadata{}, fmt.Errorf("Adoptium metadata does not match requested runtime")
	}
	if _, err := portablePath(p.Name); err != nil || strings.Contains(p.Name, "/") {
		return metadata{}, fmt.Errorf("unsafe runtime package name")
	}
	suffix := ".tar.gz"
	if hostOS() == "windows" {
		suffix = ".zip"
	}
	if !strings.HasSuffix(p.Name, suffix) {
		return metadata{}, fmt.Errorf("unexpected runtime archive format")
	}
	if p.Size <= 0 || p.Size > maxArchiveBytes {
		return metadata{}, fmt.Errorf("runtime artifact size exceeds limits")
	}
	digest, err := hex.DecodeString(p.Checksum)
	if err != nil || len(digest) != sha256.Size {
		return metadata{}, fmt.Errorf("missing valid published SHA256")
	}
	if err := allowedURL(p.Link, false); err != nil {
		return metadata{}, err
	}
	u, _ := url.Parse(p.Link)
	if u.Hostname() != "github.com" || !strings.HasPrefix(u.Path, fmt.Sprintf("/adoptium/temurin%d-binaries/releases/download/", major)) {
		return metadata{}, fmt.Errorf("runtime artifact is outside requested Temurin release repository")
	}
	v.Binary.Package.Checksum = strings.ToLower(p.Checksum)
	return v, nil
}
func (m *Manager) download(ctx context.Context, meta metadata, dest string, progress io.Writer) ([]byte, error) {
	resp, err := m.request(ctx, meta.Binary.Package.Link)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("create download staging: %w", err)
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), &contextReader{ctx: ctx, r: io.LimitReader(resp.Body, meta.Binary.Package.Size+1)})
	if err != nil {
		return nil, fmt.Errorf("download Temurin: %w", err)
	}
	if n != meta.Binary.Package.Size {
		return nil, fmt.Errorf("Temurin download size mismatch")
	}
	digest := hash.Sum(nil)
	if hex.EncodeToString(digest) != meta.Binary.Package.Checksum {
		return nil, fmt.Errorf("Temurin SHA256 mismatch")
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("sync Temurin download: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close Temurin download: %w", err)
	}
	if progress != nil {
		if _, err := fmt.Fprintf(progress, "Verified Temurin Java %d archive (%d bytes)\n", meta.Version.Major, n); err != nil {
			return nil, fmt.Errorf("write runtime progress: %w", err)
		}
	}
	return digest, nil
}

type fileDigest struct {
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}
type manifest struct {
	Schema         int                   `json:"schema"`
	Runtime        domain.JavaRuntime    `json:"runtime"`
	Files          map[string]fileDigest `json:"files"`
	VerifiedSHA256 string                `json:"verified_sha256"`
	VerifiedAt     time.Time             `json:"verified_at"`
}

func hashTree(ctx context.Context, root string) (map[string]fileDigest, error) {
	files := map[string]fileDigest{}
	var total int64
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".runtime.json" {
			return nil
		}
		if _, err := portablePath(rel); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("runtime tree contains links or special files")
		}
		if len(files) >= maxEntries || info.Size() > maxEntryBytes || info.Size() < 0 || total > maxExtractedBytes-info.Size() {
			return fmt.Errorf("runtime tree exceeds size limit")
		}
		total += info.Size()
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, &contextReader{ctx: ctx, r: io.LimitReader(f, info.Size()+1)})
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != info.Size() {
			return fmt.Errorf("runtime file changed while verifying")
		}
		files[rel] = fileDigest{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: n, Executable: info.Mode()&0111 != 0}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("verify runtime tree: %w", err)
	}
	return files, nil
}
func (m *Manager) Get(ctx context.Context, id string) (domain.JavaRuntime, error) {
	if err := ctx.Err(); err != nil {
		return domain.JavaRuntime{}, err
	}
	if !runtimeIDPattern.MatchString(id) {
		return domain.JavaRuntime{}, fmt.Errorf("invalid runtime identifier")
	}
	if err := m.checkRoot(); err != nil {
		return domain.JavaRuntime{}, err
	}
	root := filepath.Join(m.root(), id)
	info, err := os.Lstat(root)
	if err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("read runtime directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return domain.JavaRuntime{}, fmt.Errorf("runtime is not a real directory")
	}
	mf := filepath.Join(root, ".runtime.json")
	info, err = os.Lstat(mf)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return domain.JavaRuntime{}, fmt.Errorf("invalid runtime manifest")
	}
	f, err := os.Open(mf)
	if err != nil {
		return domain.JavaRuntime{}, fmt.Errorf("open runtime manifest: %w", err)
	}
	var record manifest
	decodeErr := json.NewDecoder(io.LimitReader(f, (32<<20)+1)).Decode(&record)
	_ = f.Close()
	if decodeErr != nil {
		return domain.JavaRuntime{}, fmt.Errorf("decode runtime manifest: %w", decodeErr)
	}
	r := record.Runtime
	bin := filepath.Join(root, "bin", "java")
	if r.OS == "windows" {
		bin += ".exe"
	}
	expectedID := fmt.Sprintf("temurin-%d-%s-x64-%s", r.Major, r.OS, record.VerifiedSHA256)
	if record.Schema != 1 || r.ID != id || id != expectedID || r.Vendor != "Eclipse Adoptium" || !supportedMajor(r.Major) || r.Architecture != "x64" || (r.OS != "linux" && r.OS != "windows") || r.Executable != bin || r.Artifact.Checksum.Algorithm != "sha256" || r.Artifact.Checksum.Value != record.VerifiedSHA256 || record.VerifiedAt.IsZero() || len(record.Files) == 0 || !versionPattern.MatchString(r.Version) {
		return domain.JavaRuntime{}, fmt.Errorf("runtime manifest failed validation")
	}
	if err := allowedURL(r.Artifact.SourceURL, false); err != nil {
		return domain.JavaRuntime{}, err
	}
	files, err := hashTree(ctx, root)
	if err != nil {
		return domain.JavaRuntime{}, err
	}
	if len(files) != len(record.Files) {
		return domain.JavaRuntime{}, fmt.Errorf("runtime files differ from verified installation")
	}
	for path, hash := range files {
		if original, ok := record.Files[path]; !ok || original != hash {
			return domain.JavaRuntime{}, fmt.Errorf("runtime file integrity mismatch")
		}
	}
	rel, _ := filepath.Rel(root, bin)
	if _, ok := files[filepath.ToSlash(rel)]; !ok {
		return domain.JavaRuntime{}, fmt.Errorf("runtime manifest lacks Java executable")
	}
	return r, nil
}
func (m *Manager) List(ctx context.Context) ([]domain.JavaRuntime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkRoot(); err != nil {
		if os.IsNotExist(err) {
			return []domain.JavaRuntime{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(m.root())
	if os.IsNotExist(err) {
		return []domain.JavaRuntime{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list runtimes: %w", err)
	}
	out := []domain.JavaRuntime{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			continue
		}
		if !runtimeIDPattern.MatchString(entry.Name()) {
			return nil, fmt.Errorf("unexpected entry in managed runtime root")
		}
		r, err := m.Get(ctx, entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Artifact.RetrievedAt.Equal(out[j].Artifact.RetrievedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].Artifact.RetrievedAt.After(out[j].Artifact.RetrievedAt)
	})
	return out, nil
}
