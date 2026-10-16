package loaderutil

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type Library struct {
	Name      string `json:"name"`
	Downloads struct {
		Artifact struct {
			Path string `json:"path"`
			URL  string `json:"url"`
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
		} `json:"artifact"`
	} `json:"downloads"`
	URL       string   `json:"url"`
	Checksums []string `json:"checksums"`
}
type Profile struct {
	Data map[string]struct {
		Server string `json:"server"`
	} `json:"data"`
	Processors []struct {
		Sides   []string          `json:"sides"`
		Outputs map[string]string `json:"outputs"`
	} `json:"processors"`
	Spec          int       `json:"spec"`
	Minecraft     string    `json:"minecraft"`
	Version       string    `json:"version"`
	Path          string    `json:"path"`
	JSON          string    `json:"json"`
	ServerJarPath string    `json:"serverJarPath"`
	Libraries     []Library `json:"libraries"`
}
type Version struct {
	ID           string    `json:"id"`
	InheritsFrom string    `json:"inheritsFrom"`
	MainClass    string    `json:"mainClass"`
	Libraries    []Library `json:"libraries"`
}
type Installer struct {
	Profile Profile
	Version Version
	Entries map[string][]byte
	Bytes   []byte
}

func ValidateJar(r io.ReaderAt, size int64) error { _, e := checkedZip(r, size); return e }
func checkedZip(r io.ReaderAt, size int64) (*zip.Reader, error) {
	z, e := zip.NewReader(r, size)
	if e != nil {
		return nil, errors.New("artifact is not a valid JAR")
	}
	if len(z.File) > 200000 {
		return nil, errors.New("too many JAR entries")
	}
	seen := map[string]bool{}
	var expanded uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if e = SafePath(name); e != nil {
			return nil, e
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, errors.New("duplicate or case-colliding JAR entries")
		}
		seen[key] = true
		if f.Mode()&os.ModeSymlink != 0 || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return nil, errors.New("unsafe JAR entry type")
		}
		if f.UncompressedSize64 > 1<<30 {
			return nil, errors.New("JAR entry exceeds bound")
		}
		expanded += f.UncompressedSize64
		if expanded > 4<<30 {
			return nil, errors.New("JAR expansion exceeds bound")
		}
	}
	return z, nil
}
func zipBytes(z *zip.Reader, name string, max int64) ([]byte, error) {
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		if f.UncompressedSize64 > uint64(max) {
			return nil, errors.New("installer metadata exceeds bound")
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		b, e := io.ReadAll(io.LimitReader(r, max+1))
		ce := r.Close()
		if e == nil {
			e = ce
		}
		if e != nil || int64(len(b)) > max {
			return nil, errors.New("read installer entry")
		}
		return b, nil
	}
	return nil, os.ErrNotExist
}

// InspectInstaller binds availability to metadata inside hash-verified official
// installers. Maven's version naming alone never establishes a Minecraft tuple.
func InspectInstaller(b []byte, s domain.VersionSelection) (Installer, error) {
	var out Installer
	z, e := checkedZip(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		return out, e
	}
	profile, e := zipBytes(z, "install_profile.json", 4<<20)
	if e != nil {
		return out, errors.New("installer profile is missing")
	}
	if json.Unmarshal(profile, &out.Profile) != nil || out.Profile.Minecraft != s.MinecraftVersion || out.Profile.JSON != "/version.json" || out.Profile.Spec < 0 || out.Profile.Spec > 1 {
		return out, errors.New("installer does not publish the requested Minecraft tuple")
	}
	v, e := zipBytes(z, "version.json", 4<<20)
	if e != nil || json.Unmarshal(v, &out.Version) != nil || out.Version.InheritsFrom != s.MinecraftVersion {
		return out, errors.New("installer version metadata does not match Minecraft")
	}
	if s.Flavor == domain.FlavorNeoForge {
		if out.Version.ID != "neoforge-"+s.LoaderVersion || out.Profile.Version != out.Version.ID {
			return out, errors.New("NeoForge installer version mismatch")
		}
	} else if s.Flavor == domain.FlavorForge {
		combined := s.MinecraftVersion + "-" + s.LoaderVersion
		if out.Profile.Path != "" && out.Profile.Path != "net.minecraftforge:forge:"+combined {
			return out, errors.New("Forge installer coordinate mismatch")
		}
		if out.Version.ID != s.MinecraftVersion+"-forge-"+s.LoaderVersion || out.Profile.Version != out.Version.ID {
			return out, errors.New("Forge installer version mismatch")
		}
	} else {
		return out, errors.New("unknown installer distribution")
	}
	out.Entries = map[string][]byte{"install_profile.json": profile, "version.json": v}
	for _, n := range []string{"data/unix_args.txt", "data/win_args.txt"} {
		v, e := zipBytes(z, n, 1<<20)
		if e == nil {
			out.Entries[n] = v
		} else if !os.IsNotExist(e) {
			return out, e
		}
	}
	out.Bytes = b
	return out, nil
}

// Argument files are parsed into direct argv, never run through a shell. This
// intentionally rejects unreviewed Java escape/continuation syntax and @files.
func splitJavaArgs(raw string) ([]string, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("argument file too large")
	}
	var out []string
	var word strings.Builder
	var quote rune
	comment := false
	flush := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	for _, r := range raw {
		if comment {
			if r == '\n' {
				comment = false
			}
			continue
		}
		if r == 0 || r == '\\' {
			return nil, errors.New("unreviewed argument escape syntax")
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '#':
			flush()
			comment = true
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			word.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated argument quote")
	}
	flush()
	if len(out) == 0 || len(out) > 10000 {
		return nil, errors.New("invalid argument count")
	}
	return out, nil
}
func RelocateArgs(root *os.Root, raw, final, platform string) ([]string, error) {
	args, e := splitJavaArgs(raw)
	if e != nil {
		return nil, e
	}
	sep := ":"
	if platform == "windows" {
		sep = ";"
	}
	libraryPaths := func(value string) (string, error) {
		parts := strings.Split(value, sep)
		for n, p := range parts {
			if !strings.HasPrefix(p, "libraries/") {
				return "", errors.New("launch classpath escapes immutable libraries")
			}
			if e := SafePath(p); e != nil {
				return "", e
			}
			info, e := root.Lstat(p)
			if e != nil || !info.Mode().IsRegular() {
				return "", fmt.Errorf("required launch library %q is missing; obtain it from the official publisher", p)
			}
			parts[n] = strings.TrimRight(filepath.ToSlash(final), "/") + "/" + p
		}
		return strings.Join(parts, sep), nil
	}
	for n, a := range args {
		if strings.HasPrefix(a, "@") || strings.HasPrefix(a, "-javaagent") || strings.HasPrefix(a, "-agent") || strings.HasPrefix(a, "-Xbootclasspath") {
			return nil, errors.New("unsupported loader JVM injection argument")
		}
		switch {
		case a == "-p" || a == "--module-path" || a == "-cp" || a == "-classpath":
			if n+1 >= len(args) {
				return nil, errors.New("missing path argument")
			}
			args[n+1], e = libraryPaths(args[n+1])
			if e != nil {
				return nil, e
			}
		case strings.HasPrefix(a, "-DlegacyClassPath="):
			value, e := libraryPaths(strings.TrimPrefix(a, "-DlegacyClassPath="))
			if e != nil {
				return nil, e
			}
			args[n] = "-DlegacyClassPath=" + value
		case a == "-DlibraryDirectory=libraries":
			args[n] = "-DlibraryDirectory=" + strings.TrimRight(filepath.ToSlash(final), "/") + "/libraries"
		default:
			if strings.Contains(a, "libraries/") {
				if n > 0 && (args[n-1] == "-p" || args[n-1] == "--module-path" || args[n-1] == "-cp" || args[n-1] == "-classpath") {
					continue
				}
				return nil, errors.New("unreviewed relative library argument")
			}
		}
	}
	return append(args, "nogui"), nil
}
func manifestProperties(b []byte) map[string]string {
	out := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	key := ""
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, " ") && key != "" {
			out[key] += strings.TrimPrefix(line, " ")
			continue
		}
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) == 2 {
			key = parts[0]
			out[key] = parts[1]
		} else {
			key = ""
		}
	}
	return out
}
func LegacyLaunch(root *os.Root, jar, final string) ([]string, error) {
	b, e := ReadFile(root, jar, MaxArtifact)
	if e != nil {
		return nil, e
	}
	z, e := checkedZip(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		return nil, e
	}
	manifest, e := zipBytes(z, "META-INF/MANIFEST.MF", 1<<20)
	if e != nil {
		return nil, e
	}
	properties := manifestProperties(manifest)
	if properties["Main-Class"] == "" {
		return nil, errors.New("legacy Forge JAR lacks Main-Class")
	}
	for _, ref := range strings.Fields(properties["Class-Path"]) {
		if e = SafePath(ref); e != nil {
			return nil, e
		}
		info, e := root.Lstat(ref)
		if e != nil || !info.Mode().IsRegular() {
			return nil, errors.New("legacy Forge library missing; obtain required files from official Forge downloads")
		}
	}
	return []string{"-DlibraryDirectory=" + filepath.Join(final, "libraries"), "-jar", filepath.Join(final, jar), "nogui"}, nil
}
func VerifyLibraries(ctx context.Context, root *os.Root, libraries []Library) error {
	for _, lib := range libraries {
		a := lib.Downloads.Artifact
		if a.Path == "" {
			continue
		}
		if e := SafePath(a.Path); e != nil {
			return e
		}
		if !ValidDigest("sha1", a.SHA1) || a.Size <= 0 {
			return errors.New("invalid installer library digest")
		}
		name := "libraries/" + a.Path
		if _, e := root.Lstat(name); os.IsNotExist(e) {
			continue
		} else if e != nil {
			return e
		}
		if e := VerifyFile(ctx, root, name, domain.Artifact{Size: a.Size, Checksum: domain.Checksum{Algorithm: "sha1", Value: a.SHA1}}); e != nil {
			return fmt.Errorf("verify installer library: %w", e)
		}
	}
	return nil
}
