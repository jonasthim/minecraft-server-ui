package providerutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"github.com/jonasthim/minecraft-server-ui/internal/server/launchargs"
)

var pinnedRuntime = regexp.MustCompile(`^temurin-(8|11|16|17|21|25)-(linux|windows)-x64-([a-f0-9]{64})$`)

// VerifyLaunch revalidates a sealed Vanilla/Paper publication without network or
// executing Java. The common launcher separately rehashes the managed runtime.
func VerifyLaunch(ctx context.Context, dataDir, id string, spec domain.LaunchSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, err := instance.NewPaths(dataDir)
	if err != nil {
		return err
	}
	dirs, err := paths.For(id)
	if err != nil {
		return err
	}
	if spec.WorkingDir != dirs.Server {
		return fmt.Errorf("launch working directory differs from instance")
	}
	args, err := launchargs.StripMemory(spec.Args)
	if err != nil {
		return err
	}
	if len(args) != 3 || args[0] != "-jar" || args[2] != "nogui" {
		return fmt.Errorf("JAR launch argv differs from approved provider layout")
	}
	base, err := paths.Resolve(id, "server", ".manager/artifacts")
	if err != nil {
		return err
	}
	name := filepath.Base(filepath.Dir(args[1]))
	if !hex64.MatchString(name) || args[1] != filepath.Join(base, name, "server.jar") {
		return fmt.Errorf("JAR launch is outside canonical immutable publication")
	}
	root, err := safepath.OpenRoot(ctx, base)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(name + "/manifest.json")
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("invalid JAR publication receipt")
	}
	file, err := root.Open(name + "/manifest.json")
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	var receipt manifest
	if len(raw) > 1<<20 || json.Unmarshal(raw, &receipt) != nil || receipt.Schema != 1 || receipt.VerifiedAt.IsZero() || PlanID(receipt.Plan) != name {
		return fmt.Errorf("invalid pinned JAR publication receipt")
	}
	plan := receipt.Plan
	if len(plan.Artifacts) != 1 || plan.Requirements.RequiresInstaller {
		return fmt.Errorf("JAR receipt lacks one exact server artifact")
	}
	publisher := Mojang
	switch plan.Selection.Flavor {
	case domain.FlavorVanilla:
		if plan.Selection.LoaderVersion != "" || plan.Artifacts[0].ID != "vanilla/"+plan.Selection.MinecraftVersion || plan.Artifacts[0].VersionID != plan.Selection.MinecraftVersion || plan.Artifacts[0].Filename != "minecraft_server."+plan.Selection.MinecraftVersion+".jar" {
			return fmt.Errorf("Vanilla artifact does not bind selected release")
		}
	case domain.FlavorPaper:
		publisher = Paper
		build, err := strconv.Atoi(plan.Selection.LoaderVersion)
		if err != nil || build <= 0 || strconv.Itoa(build) != plan.Selection.LoaderVersion {
			return fmt.Errorf("Paper build must be exactly pinned")
		}
		if plan.Artifacts[0].ID != "paper/"+plan.Selection.MinecraftVersion+"/"+plan.Selection.LoaderVersion || plan.Artifacts[0].VersionID != plan.Selection.MinecraftVersion+"/"+plan.Selection.LoaderVersion || plan.Artifacts[0].Filename != "paper-"+plan.Selection.MinecraftVersion+"-"+plan.Selection.LoaderVersion+".jar" {
			return fmt.Errorf("Paper artifact does not bind selected release/build")
		}
	default:
		return fmt.Errorf("receipt is not a Vanilla or Paper publication")
	}
	if err = CheckArtifact(publisher, plan.Artifacts[0]); err != nil {
		return err
	}
	required, err := managedruntime.Select(plan.Selection)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(required, plan.Requirements) {
		return fmt.Errorf("JAR receipt runtime requirements differ from reviewed tuple")
	}
	match := pinnedRuntime.FindStringSubmatch(spec.JavaRuntimeID)
	if match == nil {
		return fmt.Errorf("managed runtime identity required")
	}
	major, _ := strconv.Atoi(match[1])
	binary := "java"
	if goruntime.GOOS == "windows" {
		binary += ".exe"
	}
	if major != required.JavaMajor || match[2] != goruntime.GOOS || spec.JavaChecksum.Algorithm != "sha256" || spec.JavaChecksum.Value != match[3] || spec.Executable != filepath.Join(dataDir, "runtimes", spec.JavaRuntimeID, "bin", binary) {
		return fmt.Errorf("JAR launch runtime differs from pinned compatible managed runtime")
	}
	for _, source := range receipt.Sources {
		if err = ValidateURL(publisher, source.URL); err != nil {
			return err
		}
		if !ValidDigest("sha256", source.SHA256) || source.RetrievedAt.IsZero() {
			return fmt.Errorf("invalid publisher metadata provenance")
		}
	}
	if err = verifiedExisting(ctx, root, name, plan); err != nil {
		return err
	}
	if strings.ContainsRune(spec.Executable, 0) {
		return fmt.Errorf("invalid executable")
	}
	return nil
}
