package loaderutil

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/server/launchargs"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
)

func FabricLaunch(ctx context.Context, root *os.Root, plan domain.InstallPlan, final, main string, reuse bool) ([]string, error) {
	if len(plan.Artifacts) < 3 || len(plan.Artifacts) > 259 || plan.Artifacts[1].ID != "net.fabricmc:fabric-loader:"+plan.Selection.LoaderVersion || plan.Artifacts[2].ID != "net.fabricmc:intermediary:"+plan.Selection.MinecraftVersion {
		return nil, errors.New("Fabric artifacts do not bind the selected tuple")
	}
	if main != "net.fabricmc.loader.impl.launch.knot.KnotServer" && main != "net.fabricmc.loader.launch.knot.KnotServer" {
		return nil, errors.New("unreviewed Fabric launch class")
	}
	cp := []string{}
	for n, a := range plan.Artifacts {
		name := "server.jar"
		if n > 0 {
			p, e := MavenPath(a.ID)
			if e != nil {
				return nil, e
			}
			name = "libraries/" + p
			if a.Provider != "fabric" || a.SourceURL != "https://maven.fabricmc.net/"+p {
				return nil, errors.New("Fabric library origin differs from pinned coordinate")
			}
			cp = append(cp, filepath.Join(final, filepath.FromSlash(name)))
		} else if a.Provider != "mojang" || a.VersionID != plan.Selection.MinecraftVersion {
			return nil, errors.New("Fabric Minecraft artifact identity mismatch")
		}
		if e := VerifyFile(ctx, root, name, a); e != nil {
			return nil, e
		}
		if n == 1 {
			b, e := ReadFile(root, name, MaxArtifact)
			if e != nil {
				return nil, e
			}
			z, e := checkedZip(bytes.NewReader(b), int64(len(b)))
			if e != nil {
				return nil, e
			}
			found := false
			class := strings.ReplaceAll(main, ".", "/") + ".class"
			for _, f := range z.File {
				if f.Name == class {
					found = true
				}
			}
			if !found {
				return nil, errors.New("Fabric loader does not contain its launch class")
			}
		}
	}
	game, gameCP, e := PrepareMinecraft(ctx, root, reuse)
	if e != nil {
		return nil, e
	}
	for _, name := range gameCP {
		cp = append(cp, filepath.Join(final, filepath.FromSlash(name)))
	}
	return []string{"-Dfabric.gameJarPath=" + filepath.Join(final, filepath.FromSlash(game)), "-cp", strings.Join(cp, string(os.PathListSeparator)), main, "nogui"}, nil
}
func InstallerLaunch(ctx context.Context, root *os.Root, plan domain.InstallPlan, installer Installer, final string) ([]string, error) {
	if len(plan.Artifacts) != 2 {
		return nil, errors.New("installer plan must bind installer and Minecraft artifacts")
	}
	if e := VerifyFile(ctx, root, "installer.jar", plan.Artifacts[0]); e != nil {
		return nil, e
	}
	libs := append(append([]Library{}, installer.Profile.Libraries...), installer.Version.Libraries...)
	if e := VerifyLibraries(ctx, root, libs); e != nil {
		return nil, e
	}
	if e := VerifyGenerated(ctx, root, installer.Profile); e != nil {
		return nil, e
	}
	if e := verifyVanilla(ctx, root, installer.Profile, plan.Artifacts[1], plan.Selection.MinecraftVersion); e != nil {
		return nil, e
	}
	raw := installer.Entries["data/unix_args.txt"]
	argName := "unix_args.txt"
	if goruntime.GOOS == "windows" {
		raw = installer.Entries["data/win_args.txt"]
		argName = "win_args.txt"
	}
	if len(raw) > 0 {
		mavenPath := "net/minecraftforge/forge"
		version := plan.Selection.MinecraftVersion + "-" + plan.Selection.LoaderVersion
		if plan.Selection.Flavor == domain.FlavorNeoForge {
			mavenPath = "net/neoforged/neoforge"
			version = plan.Selection.LoaderVersion
		}
		name := "libraries/" + mavenPath + "/" + version + "/" + argName
		actual, e := ReadFile(root, name, 1<<20)
		if e != nil || !bytes.Equal(actual, raw) {
			return nil, errors.New("installed arguments differ from the verified installer")
		}
		args, e := RelocateArgs(root, string(raw), final, goruntime.GOOS)
		if e != nil {
			return nil, e
		}
		if !containsArg(args, installer.Version.MainClass) || !containsArg(args, "forgeserver") {
			return nil, errors.New("installer has an unreviewed server launch target")
		}
		return args, nil
	}
	if plan.Selection.Flavor == domain.FlavorNeoForge {
		return nil, errors.New("NeoForge server arguments unavailable; use official NeoForge downloads")
	}
	return LegacyLaunch(root, "forge-"+plan.Selection.MinecraftVersion+"-"+plan.Selection.LoaderVersion+".jar", final)
}

// VerifyLaunch checks an already-published proposal entirely offline. The native
// launcher separately calls runtime.Manager.Get before invoking this verifier.
// Only coordinator-owned canonical memory prefixes may extend provider argv.
func VerifyLaunch(ctx context.Context, dataDir, id string, spec domain.LaunchSpec) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if len(spec.Env) != 0 {
		return errors.New("loader launch environment overrides are forbidden")
	}
	args, e := launchargs.StripMemory(spec.Args)
	if e != nil {
		return e
	}
	paths, e := instance.NewPaths(dataDir)
	if e != nil {
		return e
	}
	dirs, e := paths.For(id)
	if e != nil {
		return e
	}
	if spec.WorkingDir != dirs.Server {
		return errors.New("loader working directory is not the canonical instance server")
	}
	base, e := paths.Resolve(id, "server", ".manager/loaders")
	if e != nil {
		return e
	}
	prefix := filepath.ToSlash(base) + "/"
	name := ""
	for _, arg := range args {
		normalized := filepath.ToSlash(arg)
		rest := normalized
		for {
			at := strings.Index(rest, prefix)
			if at < 0 {
				break
			}
			rest = rest[at+len(prefix):]
			if len(rest) < 65 || rest[64] != '/' {
				return errors.New("invalid immutable loader path")
			}
			candidate := rest[:64]
			decoded, e := hex.DecodeString(candidate)
			if e != nil || len(decoded) != 32 || strings.ToLower(candidate) != candidate {
				return errors.New("invalid immutable loader identifier")
			}
			if name != "" && name != candidate {
				return errors.New("mixed loader release references")
			}
			name = candidate
			rest = rest[64:]
		}
	}
	if name == "" {
		return errors.New("loader launch has no immutable release reference")
	}
	final, e := paths.Resolve(id, "server", ".manager/loaders/"+name)
	if e != nil {
		return e
	}
	data, e := os.OpenRoot(dataDir)
	if e != nil {
		return e
	}
	defer data.Close()
	root, e := data.OpenRoot(filepath.Join("instances", id, "server", ".manager", "loaders", name))
	if e != nil {
		return e
	}
	defer root.Close()
	b, e := ReadFile(root, "manifest.json", 16<<20)
	if e != nil {
		return e
	}
	var m receipt
	if json.Unmarshal(b, &m) != nil || m.Schema != 2 || m.VerifiedAt.IsZero() || providerutil.PlanID(m.Plan) != name {
		return errors.New("invalid immutable loader receipt")
	}
	req, e := managedruntime.Select(m.Plan.Selection)
	if e != nil || req != m.Plan.Requirements {
		return errors.New("receipt has unreviewed Java requirements")
	}
	if m.Runtime.ID != spec.JavaRuntimeID || m.Runtime.Executable != spec.Executable || m.Runtime.Artifact.Checksum != spec.JavaChecksum || m.Runtime.Major != req.JavaMajor {
		return errors.New("loader launch differs from bound managed runtime")
	}
	files, e := inventory(ctx, root)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(files, m.Files) {
		return errors.New("immutable loader inventory changed")
	}
	for _, a := range m.Plan.Artifacts {
		if !ValidDigest(a.Checksum.Algorithm, a.Checksum.Value) || ValidURL(a.SourceURL) != nil || a.Size <= 0 || a.Size > MaxArtifact {
			return errors.New("invalid loader artifact provenance")
		}
	}
	var rebuilt []string
	switch m.Plan.Selection.Flavor {
	case domain.FlavorFabric:
		if len(m.Args) < 2 {
			return errors.New("missing Fabric argv")
		}
		rebuilt, e = FabricLaunch(ctx, root, m.Plan, final, m.Args[len(m.Args)-2], true)
	case domain.FlavorForge, domain.FlavorNeoForge:
		if len(m.Plan.Artifacts) != 2 {
			return errors.New("invalid installer artifact plan")
		}
		b, e = ReadFile(root, "installer.jar", 64<<20)
		if e != nil {
			return e
		}
		var installer Installer
		installer, e = InspectInstaller(b, m.Plan.Selection)
		if e == nil {
			rebuilt, e = InstallerLaunch(ctx, root, m.Plan, installer, final)
		}
	default:
		return errors.New("receipt is not a supported loader distribution")
	}
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(rebuilt, m.Args) || !reflect.DeepEqual(rebuilt, args) {
		return errors.New("loader launch arguments differ from verified provider artifacts")
	}
	return nil
}
