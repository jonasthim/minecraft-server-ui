package loaderutil

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
	"github.com/jonasthim/minecraft-server-ui/internal/server/vanilla"
)

// InstallerProvider is shared only by the official Forge and NeoForge providers.
type InstallerProvider struct {
	flavor    domain.Flavor
	client    *Client
	publisher *Publisher
	vanilla   *vanilla.Provider
}

func NewInstallerProvider(dir string, httpClient *http.Client, flavor domain.Flavor) *InstallerProvider {
	c := NewClient(httpClient)
	return &InstallerProvider{flavor, c, NewPublisher(dir, c), vanilla.New(dir, httpClient)}
}
func (p *InstallerProvider) Flavor() domain.Flavor { return p.flavor }
func (p *InstallerProvider) maven() (string, string, string) {
	if p.flavor == domain.FlavorForge {
		return "https://maven.minecraftforge.net/", "net.minecraftforge:forge:", "net/minecraftforge/forge"
	}
	return "https://maven.neoforged.net/releases/", "net.neoforged:neoforge:", "net/neoforged/neoforge"
}
func (p *InstallerProvider) catalog(ctx context.Context) ([]string, Source, error) {
	base, _, path := p.maven()
	u := base + path + "/maven-metadata.xml"
	b, e := p.client.Bytes(ctx, u, 8<<20)
	source := Source{u, Digest("sha256", b), time.Now().UTC()}
	if e != nil {
		return nil, source, e
	}
	var m struct {
		XMLName  xml.Name `xml:"metadata"`
		Group    string   `xml:"groupId"`
		Artifact string   `xml:"artifactId"`
		Versions []string `xml:"versioning>versions>version"`
	}
	if xml.Unmarshal(b, &m) != nil || m.XMLName.Local != "metadata" || len(m.Versions) == 0 {
		return nil, source, errors.New("malformed official Maven catalog")
	}
	expectedGroup := "net.minecraftforge"
	expectedArtifact := "forge"
	if p.flavor == domain.FlavorNeoForge {
		expectedGroup = "net.neoforged"
		expectedArtifact = "neoforge"
	}
	if m.Group != expectedGroup || m.Artifact != expectedArtifact {
		return nil, source, errors.New("Maven catalog publisher mismatch")
	}
	seen := map[string]bool{}
	for _, v := range m.Versions {
		if !ExactVersion(v) || seen[v] {
			return nil, source, errors.New("invalid or duplicate Maven version")
		}
		seen[v] = true
	}
	return m.Versions, source, nil
}

// Versions returns published Minecraft candidates. Resolve verifies the exact
// installer-internal Minecraft identity before accepting any selected tuple.
func (p *InstallerProvider) Versions(ctx context.Context) ([]string, error) {
	versions, _, e := p.catalog(ctx)
	if e != nil {
		return nil, e
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range versions {
		mc := ""
		if p.flavor == domain.FlavorForge {
			parts := strings.SplitN(v, "-", 2)
			if len(parts) == 2 {
				mc = parts[0]
			}
		} else { // NeoForge names are catalog candidates only, never a compatibility check.
			parts := strings.Split(v, ".")
			if len(parts) >= 2 {
				if parts[0] == "20" {
					mc = "1.20." + parts[1]
				} else if parts[0] == "26" {
					mc = "26." + parts[1]
				} else if parts[0] == "21" {
					mc = "1.21"
					if parts[1] != "0" {
						mc += "." + parts[1]
					}
				}
			}
		}
		if _, e = managedruntime.Select(domain.VersionSelection{Flavor: p.flavor, MinecraftVersion: mc}); e != nil || seen[mc] {
			continue
		}
		seen[mc] = true
		out = append(out, mc)
	}
	providerutil.SortVersions(out)
	return out, nil
}

// Loaders returns exact Maven versions. NeoForge candidates are checked against
// installer metadata by Resolve; naming alone is explicitly insufficient.
func (p *InstallerProvider) Loaders(ctx context.Context, mc string) ([]string, error) {
	if _, e := managedruntime.Select(domain.VersionSelection{Flavor: p.flavor, MinecraftVersion: mc}); e != nil {
		return nil, e
	}
	versions, _, e := p.catalog(ctx)
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, v := range versions {
		if p.flavor == domain.FlavorForge {
			prefix := mc + "-"
			if strings.HasPrefix(v, prefix) {
				out = append(out, strings.TrimPrefix(v, prefix))
			}
		} else { // Exact tuple verification still happens in Resolve.
			prefix := strings.TrimPrefix(mc, "1.")
			if strings.Count(prefix, ".") == 1 {
				parts := strings.Split(prefix, ".")
				prefix = parts[0] + "." + parts[1] + "."
			} else {
				prefix += ".0."
			}
			if strings.HasPrefix(v, prefix) {
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return newerLoader(out[i], out[j]) })
	return out, nil
}
func (p *InstallerProvider) resolve(ctx context.Context, s domain.VersionSelection) (domain.InstallPlan, []Source, Installer, error) {
	fail := func(e error) (domain.InstallPlan, []Source, Installer, error) {
		return domain.InstallPlan{}, nil, Installer{}, e
	}
	if s.Flavor != p.flavor || !ExactVersion(s.LoaderVersion) {
		return fail(errors.New("an exact published loader version is required"))
	}
	req, e := managedruntime.Select(s)
	if e != nil {
		return fail(e)
	}
	versions, source, e := p.catalog(ctx)
	if e != nil {
		return fail(e)
	}
	base, prefix, _ := p.maven()
	version := s.LoaderVersion
	if p.flavor == domain.FlavorForge {
		version = s.MinecraftVersion + "-" + s.LoaderVersion
	}
	found := false
	for _, v := range versions {
		if v == version {
			found = true
			break
		}
	}
	if !found {
		return fail(errors.New("exact loader build is absent from the official Maven catalog"))
	}
	artifact, sources, e := p.client.Artifact(ctx, string(s.Flavor), prefix+version+":installer", base)
	if e != nil {
		return fail(e)
	}
	if artifact.Size > 64<<20 {
		return fail(errors.New("installer exceeds size bound"))
	}
	var b bytes.Buffer
	if _, e = p.client.Download(ctx, artifact, &b); e != nil {
		return fail(e)
	}
	installer, e := InspectInstaller(b.Bytes(), s)
	if e != nil {
		return fail(e)
	}
	vanillaPlan, e := p.vanilla.Resolve(ctx, domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: s.MinecraftVersion})
	if e != nil {
		return fail(e)
	}
	capabilities := providerutil.Capabilities(s)
	capabilities.Mods = domain.Availability{Available: true}
	capabilities.Plugins = domain.Availability{Reason: "Forge-family loaders do not natively load server plugins"}
	plan := domain.InstallPlan{Selection: s, Requirements: req, Artifacts: append([]domain.Artifact{artifact}, vanillaPlan.Artifacts...), Capabilities: capabilities, Warnings: []string{"Publisher tuple availability does not establish tested server/client support.", "The verified publisher installer downloads and generates additional executable libraries; their receipt records installer provenance and local SHA256."}}
	return plan, append([]Source{source}, sources...), installer, nil
}
func (p *InstallerProvider) Resolve(ctx context.Context, s domain.VersionSelection) (domain.InstallPlan, error) {
	plan, _, _, e := p.resolve(ctx, s)
	return plan, e
}
func (p *InstallerProvider) Install(ctx context.Context, id string, plan domain.InstallPlan, r domain.JavaRuntime, w io.Writer) (domain.LaunchSpec, error) {
	canonical, sources, installer, e := p.resolve(ctx, plan.Selection)
	if e != nil {
		return domain.LaunchSpec{}, e
	}
	if !providerutil.SamePlan(plan, canonical) {
		return domain.LaunchSpec{}, errors.New("loader plan differs from official pinned artifacts")
	}
	build := func(ctx context.Context, root *os.Root, stage, final string, reuse bool) ([]string, error) {
		if !reuse {
			if e := WriteFile(root, "installer.jar", installer.Bytes); e != nil {
				return nil, e
			}
			if e := runInstaller(ctx, r, root, stage, w); e != nil {
				return nil, e
			}
		}
		return InstallerLaunch(ctx, root, canonical, installer, final)
	}
	return p.publisher.Publish(ctx, id, canonical, r, sources, w, build)
}
func containsArg(args []string, v string) bool {
	if v == "" {
		return false
	}
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}
func verifyVanilla(ctx context.Context, root *os.Root, p Profile, a domain.Artifact, mc string) error {
	candidates := []string{"minecraft_server." + mc + ".jar", "server.jar", "libraries/net/minecraft/server/" + mc + "/server-" + mc + ".jar"}
	for _, name := range candidates {
		if _, e := root.Lstat(name); e == nil {
			if e := VerifyFile(ctx, root, name, a); e != nil {
				return e
			}
			return VerifyBundledLibraries(ctx, root, name)
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	return errors.New("installer did not preserve the publisher-verified vanilla server; use the official installer download")
}

type boundedLog struct {
	w         io.Writer
	remaining int64
}

func (l *boundedLog) Write(b []byte) (int, error) {
	n := len(b)
	if l.remaining > 0 {
		p := b
		if int64(len(p)) > l.remaining {
			p = p[:l.remaining]
		}
		written, e := l.w.Write(p)
		l.remaining -= int64(written)
		if e != nil {
			return 0, e
		}
	}
	return n, nil
}
func installerEnv(r domain.JavaRuntime, stage string) []string {
	env := []string{"JAVA_HOME=" + filepath.Dir(filepath.Dir(r.Executable)), "PATH=" + filepath.Dir(r.Executable), "HOME=" + filepath.Join(stage, ".home"), "USERPROFILE=" + filepath.Join(stage, ".home"), "TMPDIR=" + filepath.Join(stage, ".tmp"), "TMP=" + filepath.Join(stage, ".tmp"), "TEMP=" + filepath.Join(stage, ".tmp"), "LANG=C.UTF-8"}
	for _, k := range []string{"SYSTEMROOT", "WINDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
func runInstaller(ctx context.Context, r domain.JavaRuntime, root *os.Root, stage string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if e := root.Mkdir(".home", 0700); e != nil {
		return e
	}
	if e := root.Mkdir(".tmp", 0700); e != nil {
		return e
	}
	log, e := root.OpenFile("installer-output.txt", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	sink := &boundedLog{log, 4 << 20}
	cmd := exec.Command(r.Executable, "-Djava.awt.headless=true", "-jar", filepath.Join(stage, "installer.jar"), "--installServer", stage)
	cmd.Dir = stage
	cmd.Env = installerEnv(r, stage)
	cmd.Stdout = sink
	cmd.Stderr = sink
	if w != nil {
		_, _ = io.WriteString(w, "Running the hash-verified official loader installer in isolated staging.\n")
	}
	if e = runConfined(ctx, cmd); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("official loader installer failed; required files may be unavailable: use official %s downloads", "publisher")
	}
	if e = log.Sync(); e != nil {
		return e
	} // Scripts are publisher outputs, never executed and never used to launch.
	for _, name := range []string{"run.sh", "run.bat", "user_jvm_args.txt", ".home", ".tmp", "installer.jar.log"} {
		if e = root.RemoveAll(name); e != nil {
			return e
		}
	}
	return nil
}

var loaderParts = regexp.MustCompile(`[.-]`)

func newerLoader(a, b string) bool {
	left, right := loaderParts.Split(a, -1), loaderParts.Split(b, -1)
	for i := 0; i < len(left) && i < len(right); i++ {
		x, xe := strconv.Atoi(left[i])
		y, ye := strconv.Atoi(right[i])
		if xe == nil && ye == nil {
			if x != y {
				return x > y
			}
		} else if left[i] != right[i] {
			return left[i] > right[i]
		}
	}
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	return a > b
}
func (p *InstallerProvider) ManualDownloadURL() string {
	if p.flavor == domain.FlavorForge {
		return "https://files.minecraftforge.net/"
	}
	return "https://neoforged.net/"
}
