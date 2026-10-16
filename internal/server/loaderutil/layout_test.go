package loaderutil

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func jarFixture(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for n, v := range entries {
		f, e := z.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte(v)); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestInspectInstallerBindsPublishedTuple(t *testing.T) {
	b := jarFixture(t, map[string]string{"META-INF/MANIFEST.MF": "Main-Class: official.Installer\n", "install_profile.json": `{"spec":1,"minecraft":"1.21.1","version":"neoforge-21.1.172","json":"/version.json"}`, "version.json": `{"id":"neoforge-21.1.172","inheritsFrom":"1.21.1"}`, "data/unix_args.txt": "-p libraries/a.jar\nmain.Server\n--launchTarget forgeserver"})
	s := domain.VersionSelection{Flavor: domain.FlavorNeoForge, MinecraftVersion: "1.21.1", LoaderVersion: "21.1.172"}
	if _, e := InspectInstaller(b, s); e != nil {
		t.Fatal(e)
	}
	s.MinecraftVersion = "1.21.2"
	if _, e := InspectInstaller(b, s); e == nil {
		t.Fatal("unpublished tuple accepted")
	}
}
func TestUnsafeInstallerArchive(t *testing.T) {
	b := jarFixture(t, map[string]string{"../escape": "x", "install_profile.json": "{}"})
	if _, e := InspectInstaller(b, domain.VersionSelection{}); e == nil {
		t.Fatal("unsafe jar accepted")
	}
}
func TestRelocateArgsLinuxWindows(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			stage := t.TempDir()
			root, e := os.OpenRoot(stage)
			if e != nil {
				t.Fatal(e)
			}
			defer root.Close()
			if e = os.MkdirAll(filepath.Join(stage, "libraries"), 0700); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(stage, "libraries", "a.jar"), []byte("x"), 0600); e != nil {
				t.Fatal(e)
			}
			sep := ":"
			final := "/srv/minecraft release"
			if platform == "windows" {
				sep = ";"
				final = "C:/Minecraft release"
			}
			raw := "-p libraries/a.jar" + sep + "libraries/a.jar\n-DlibraryDirectory=libraries\n-DlegacyClassPath=libraries/a.jar\nmain.Server\n--launchTarget forgeserver"
			args, e := RelocateArgs(root, raw, final, platform)
			if e != nil {
				t.Fatal(e)
			}
			joined := strings.Join(args, "\n")
			if !strings.Contains(joined, final+"/libraries/a.jar") || strings.Contains(joined, "=libraries") {
				t.Fatal(args)
			}
			if _, e = RelocateArgs(root, "-p libraries/../escape\nmain.Server", final, platform); e == nil {
				t.Fatal("unsafe launch path accepted")
			}
		})
	}
}
func TestPublishPreservesWorldAndRejectsTamper(t *testing.T) {
	dir := t.TempDir()
	server := filepath.Join(dir, "instances", "test", "server")
	if e := os.MkdirAll(filepath.Join(server, "world"), 0700); e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"world/level.dat", "server.properties", "eula.txt"} {
		if e := os.WriteFile(filepath.Join(server, n), []byte("preserve"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	r := domain.JavaRuntime{ID: "managed", Major: 17, Executable: filepath.Join(dir, "java")}
	p := NewPublisher(dir, NewClient(nil))
	p.getRuntime = func(context.Context, string) (domain.JavaRuntime, error) { return r, nil }
	plan := domain.InstallPlan{Selection: domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.1", LoaderVersion: "0.16.14"}, Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17, MaxJavaMajor: 17}}
	build := func(ctx context.Context, root *os.Root, stage, final string, reuse bool) ([]string, error) {
		if !reuse {
			if e := WriteFile(root, "server.jar", []byte("verified executable")); e != nil {
				return nil, e
			}
		}
		return []string{"-jar", filepath.Join(final, "server.jar"), "nogui"}, nil
	}
	spec, e := p.Publish(context.Background(), "test", plan, r, nil, nil, build)
	if e != nil {
		t.Fatal(e)
	}
	if spec.WorkingDir != server {
		t.Fatal(spec)
	}
	for _, n := range []string{"world/level.dat", "server.properties", "eula.txt"} {
		b, e := os.ReadFile(filepath.Join(server, n))
		if e != nil || string(b) != "preserve" {
			t.Fatal(n, e)
		}
	}
	if _, e = os.Stat(filepath.Join(dir, "instances", "test", "state", "launch.json")); !os.IsNotExist(e) {
		t.Fatal("state changed")
	}
	if e = os.WriteFile(spec.Args[1], []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Publish(context.Background(), "test", plan, r, nil, nil, build); e == nil {
		t.Fatal("tampered cached release accepted")
	}
}
func TestCanceledPublicationCleansStage(t *testing.T) {
	dir := t.TempDir()
	if e := os.MkdirAll(filepath.Join(dir, "instances", "test", "server"), 0700); e != nil {
		t.Fatal(e)
	}
	r := domain.JavaRuntime{ID: "managed", Major: 17, Executable: filepath.Join(dir, "java")}
	p := NewPublisher(dir, NewClient(nil))
	p.getRuntime = func(context.Context, string) (domain.JavaRuntime, error) { return r, nil }
	ctx, cancel := context.WithCancel(context.Background())
	plan := domain.InstallPlan{Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17, MaxJavaMajor: 17}}
	_, e := p.Publish(ctx, "test", plan, r, nil, nil, func(ctx context.Context, root *os.Root, stage, final string, reuse bool) ([]string, error) {
		if e := WriteFile(root, "partial", []byte("partial")); e != nil {
			return nil, e
		}
		cancel()
		return nil, ctx.Err()
	})
	if e == nil {
		t.Fatal("cancel ignored")
	}
	entries, e := os.ReadDir(filepath.Join(dir, "instances", "test", "server", ".manager", "loaders"))
	if e != nil || len(entries) != 0 {
		t.Fatal("stage left behind", e, entries)
	}
}

func TestBundlerExtractionChecksInternalHashes(t *testing.T) {
	game := jarFixture(t, map[string]string{"net/minecraft/server/Main.class": "class"})
	lib := jarFixture(t, map[string]string{"lib/Class.class": "class"})
	outer := jarFixture(t, map[string]string{"META-INF/versions.list": Digest("sha256", game) + "\t1.20.1\t1.20.1/server.jar\n", "META-INF/libraries.list": Digest("sha256", lib) + "\ta:b:1\ta/b/1/b-1.jar\n", "META-INF/versions/1.20.1/server.jar": string(game), "META-INF/libraries/a/b/1/b-1.jar": string(lib)})
	dir := t.TempDir()
	root, e := os.OpenRoot(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = WriteFile(root, "server.jar", outer); e != nil {
		t.Fatal(e)
	}
	main, cp, e := PrepareMinecraft(context.Background(), root, false)
	if e != nil || main != "minecraft/versions/1.20.1/server.jar" || len(cp) != 2 {
		t.Fatal(main, cp, e)
	}
	if e = os.WriteFile(filepath.Join(dir, "minecraft/versions/1.20.1/server.jar"), []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = PrepareMinecraft(context.Background(), root, true); e == nil {
		t.Fatal("tampered nested game jar accepted")
	}
}

func TestOfficialArgumentFixtures(t *testing.T) {
	for _, flavor := range []string{"forge", "neoforge"} {
		for _, platform := range []string{"linux", "windows"} {
			t.Run(flavor+"/"+platform, func(t *testing.T) {
				name := "unix_args.txt"
				sep := ":"
				if platform == "windows" {
					name = "win_args.txt"
					sep = ";"
				}
				raw, e := os.ReadFile(filepath.Join("testdata", flavor, name))
				if e != nil {
					t.Fatal(e)
				}
				args, e := splitJavaArgs(string(raw))
				if e != nil {
					t.Fatal(e)
				}
				dir := t.TempDir()
				root, e := os.OpenRoot(dir)
				if e != nil {
					t.Fatal(e)
				}
				defer root.Close()
				for _, arg := range args {
					paths := ""
					if strings.HasPrefix(arg, "libraries/") {
						paths = arg
					} else if strings.HasPrefix(arg, "-DlegacyClassPath=") {
						paths = strings.TrimPrefix(arg, "-DlegacyClassPath=")
					}
					for _, p := range strings.Split(paths, sep) {
						if p == "" {
							continue
						}
						if e := SafePath(p); e != nil {
							t.Fatal(e)
						}
						if e := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0700); e != nil {
							t.Fatal(e)
						}
						if e := os.WriteFile(filepath.Join(dir, p), []byte("fixture path"), 0600); e != nil {
							t.Fatal(e)
						}
					}
				}
				final := "/srv/Minecraft release"
				if platform == "windows" {
					final = "C:/Minecraft release"
				}
				relocated, e := RelocateArgs(root, string(raw), final, platform)
				if e != nil {
					t.Fatal(e)
				}
				if !containsArg(relocated, "forgeserver") || relocated[len(relocated)-1] != "nogui" {
					t.Fatal(relocated)
				}
			})
		}
	}
}
func TestPlausibleNeoForgeNameStillRequiresInstallerIdentity(t *testing.T) {
	identity, e := os.ReadFile("testdata/neoforge/install_profile.identity.json")
	if e != nil {
		t.Fatal(e)
	}
	version, e := os.ReadFile("testdata/neoforge/version.json")
	if e != nil {
		t.Fatal(e)
	}
	b := jarFixture(t, map[string]string{"install_profile.json": string(identity), "version.json": string(version)})
	_, e = InspectInstaller(b, domain.VersionSelection{Flavor: domain.FlavorNeoForge, MinecraftVersion: "1.21.2", LoaderVersion: "21.1.172"})
	if e == nil {
		t.Fatal("plausible loader naming overrode publisher identity")
	}
}
func TestMissingInstanceAndRuntimeMismatchCannotMutate(t *testing.T) {
	dir := t.TempDir()
	r := domain.JavaRuntime{ID: "managed", Major: 17, Executable: filepath.Join(dir, "java")}
	p := NewPublisher(dir, NewClient(nil))
	p.getRuntime = func(context.Context, string) (domain.JavaRuntime, error) { return r, nil }
	plan := domain.InstallPlan{Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17, MaxJavaMajor: 17}}
	build := func(context.Context, *os.Root, string, string, bool) ([]string, error) {
		t.Fatal("builder ran before admission checks")
		return nil, nil
	}
	for _, id := range []string{"test", "../escape"} {
		if _, e := p.Publish(context.Background(), id, plan, r, nil, nil, build); e == nil {
			t.Fatal("unprovisioned instance accepted")
		}
	}
	r.Major = 21
	if _, e := p.Publish(context.Background(), "test", plan, r, nil, nil, build); e == nil {
		t.Fatal("wrong runtime accepted")
	}
	if _, e := os.Stat(filepath.Join(dir, "instances")); !os.IsNotExist(e) {
		t.Fatal("provider provisioned instance")
	}
}
func TestEnvironmentInjectionIsRemoved(t *testing.T) {
	for _, k := range []string{"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "LD_PRELOAD", "LD_LIBRARY_PATH", "CLASSPATH", "SECRET_TOKEN"} {
		t.Setenv(k, "injected")
	}
	env := installerEnv(domain.JavaRuntime{Executable: "/managed/bin/java"}, "/private-stage")
	for _, entry := range env {
		if strings.Contains(entry, "injected") {
			t.Fatal(entry)
		}
	}
}
func TestUnsafeOrMissingArgReferences(t *testing.T) {
	root, e := os.OpenRoot(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	for _, raw := range []string{"-p libraries/missing.jar\nmain.Server", "@another.txt", "-javaagent:evil.jar main.Server", "-p ../outside.jar main.Server", "-p libraries/CON main.Server", "-p \"unterminated"} {
		if _, e := RelocateArgs(root, raw, "/release", "linux"); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestOfflineVerifierRebuildsArgsAndBindsRuntime(t *testing.T) {
	dir := t.TempDir()
	if e := os.MkdirAll(filepath.Join(dir, "instances", "test", "server"), 0700); e != nil {
		t.Fatal(e)
	}
	r := domain.JavaRuntime{ID: "managed", Major: 17, Executable: filepath.Join(dir, "java"), Artifact: domain.Artifact{Checksum: domain.Checksum{Algorithm: "sha256", Value: strings.Repeat("a", 64)}}}
	p := NewPublisher(dir, NewClient(nil))
	p.getRuntime = func(context.Context, string) (domain.JavaRuntime, error) { return r, nil }
	main := "net.fabricmc.loader.impl.launch.knot.KnotServer"
	jars := [][]byte{jarFixture(t, map[string]string{"net/minecraft/server/Main.class": "fixture"}), jarFixture(t, map[string]string{strings.ReplaceAll(main, ".", "/") + ".class": "fixture"}), jarFixture(t, map[string]string{"mappings/mappings.tiny": "fixture"})}
	plan := domain.InstallPlan{Selection: domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.1", LoaderVersion: "0.16.14"}, Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17, MaxJavaMajor: 17}}
	for n, id := range []string{"vanilla/1.20.1", "net.fabricmc:fabric-loader:0.16.14", "net.fabricmc:intermediary:1.20.1"} {
		a := domain.Artifact{ID: id, Provider: "fabric", Size: int64(len(jars[n])), Checksum: domain.Checksum{Algorithm: "sha256", Value: Digest("sha256", jars[n])}}
		if n == 0 {
			a.Provider = "mojang"
			a.VersionID = "1.20.1"
			a.SourceURL = "https://piston-data.mojang.com/v1/objects/" + strings.Repeat("a", 40) + "/server.jar"
		} else {
			path, e := MavenPath(id)
			if e != nil {
				t.Fatal(e)
			}
			a.SourceURL = "https://maven.fabricmc.net/" + path
		}
		plan.Artifacts = append(plan.Artifacts, a)
	}
	spec, e := p.Publish(context.Background(), "test", plan, r, nil, nil, func(ctx context.Context, root *os.Root, stage, final string, reuse bool) ([]string, error) {
		if !reuse {
			for n, a := range plan.Artifacts {
				name := "server.jar"
				if n > 0 {
					p, e := MavenPath(a.ID)
					if e != nil {
						return nil, e
					}
					name = "libraries/" + p
				}
				if e := WriteFile(root, name, jars[n]); e != nil {
					return nil, e
				}
			}
		}
		return FabricLaunch(ctx, root, plan, final, main, true)
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifyLaunch(context.Background(), dir, "test", spec); e != nil {
		t.Fatal(e)
	}
	spec.Args = append([]string{"-Xms512M", "-Xmx1024M"}, spec.Args...)
	if e = VerifyLaunch(context.Background(), dir, "test", spec); e != nil {
		t.Fatal(e)
	}
	spec.JavaRuntimeID = "other"
	if e = VerifyLaunch(context.Background(), dir, "test", spec); e == nil {
		t.Fatal("runtime binding bypassed")
	}
	spec.JavaRuntimeID = r.ID
	spec.Args = append([]string{"-javaagent:injected.jar"}, spec.Args...)
	if e = VerifyLaunch(context.Background(), dir, "test", spec); e == nil {
		t.Fatal("arbitrary JVM argument accepted")
	}
}

func TestGeneratedOutputPublisherHashes(t *testing.T) {
	dir := t.TempDir()
	root, e := os.OpenRoot(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	data := []byte("publisher-generated jar")
	path, e := MavenPath("net.minecraft:server:1.20.1-build:extra")
	if e != nil {
		t.Fatal(e)
	}
	if e = WriteFile(root, "libraries/"+path, data); e != nil {
		t.Fatal(e)
	}
	var profile Profile
	raw := `{"data":{"MC_EXTRA":{"server":"[net.minecraft:server:1.20.1-build:extra]"},"MC_EXTRA_SHA":{"server":"'` + Digest("sha1", data) + `'"}},"processors":[{"sides":["server"],"outputs":{"{MC_EXTRA}":"{MC_EXTRA_SHA}"}}]}`
	if e = json.Unmarshal([]byte(raw), &profile); e != nil {
		t.Fatal(e)
	}
	if e = VerifyGenerated(context.Background(), root, profile); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "libraries", path), []byte("wrong"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = VerifyGenerated(context.Background(), root, profile); e == nil {
		t.Fatal("publisher output hash ignored")
	}
}
func TestInstallerProcessCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInstallerProcessHelper$")
	cmd.Env = append(os.Environ(), "LOADER_PROCESS_HELPER=1")
	start := time.Now()
	if e := runConfined(ctx, cmd); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("installer process did not stop")
	}
}
func TestInstallerProcessHelper(t *testing.T) {
	if os.Getenv("LOADER_PROCESS_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestForgeVersionBindingIsExact(t *testing.T) {
	b := jarFixture(t, map[string]string{"install_profile.json": `{"spec":1,"minecraft":"1.20.1","version":"1.20.1-forge-unreviewed-47.4.0","json":"/version.json"}`, "version.json": `{"id":"1.20.1-forge-unreviewed-47.4.0","inheritsFrom":"1.20.1"}`})
	if _, e := InspectInstaller(b, domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: "1.20.1", LoaderVersion: "47.4.0"}); e == nil {
		t.Fatal("loader version substring accepted instead of exact identity")
	}
}

func TestCachedSymlinksAndExtraExecutablesRejected(t *testing.T) {
	for _, mode := range []string{"symlink", "extra"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if e := os.MkdirAll(filepath.Join(dir, "instances", "test", "server"), 0700); e != nil {
				t.Fatal(e)
			}
			r := domain.JavaRuntime{ID: "managed", Major: 17, Executable: filepath.Join(dir, "java")}
			p := NewPublisher(dir, NewClient(nil))
			p.getRuntime = func(context.Context, string) (domain.JavaRuntime, error) { return r, nil }
			plan := domain.InstallPlan{Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17, MaxJavaMajor: 17}}
			build := func(ctx context.Context, root *os.Root, stage, final string, reuse bool) ([]string, error) {
				if !reuse {
					if e := WriteFile(root, "server.jar", []byte("verified")); e != nil {
						return nil, e
					}
				}
				return []string{"-jar", filepath.Join(final, "server.jar"), "nogui"}, nil
			}
			spec, e := p.Publish(context.Background(), "test", plan, r, nil, nil, build)
			if e != nil {
				t.Fatal(e)
			}
			release := filepath.Dir(spec.Args[1])
			if mode == "symlink" {
				if e = os.Symlink(spec.Args[1], filepath.Join(release, "evil.jar")); e != nil {
					t.Skip(e)
				}
			} else if e = os.WriteFile(filepath.Join(release, "evil.jar"), []byte("extra executable"), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = p.Publish(context.Background(), "test", plan, r, nil, nil, build); e == nil {
				t.Fatal("modified release accepted")
			}
		})
	}
}

func TestLegacySignedManifestLargerThan64KiB(t *testing.T) {
	root, e := os.OpenRoot(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	manifest := "Manifest-Version: 1.0\r\nMain-Class: official.LegacyServer\r\n\r\n" + strings.Repeat("Name: fixture/Class.class\r\nSHA-256-Digest: digest\r\n\r\n", 4000)
	b := jarFixture(t, map[string]string{"META-INF/MANIFEST.MF": manifest, "official/LegacyServer.class": "fixture"})
	if e = WriteFile(root, "forge.jar", b); e != nil {
		t.Fatal(e)
	}
	if _, e = LegacyLaunch(root, "forge.jar", "/immutable"); e != nil {
		t.Fatal(e)
	}
}
