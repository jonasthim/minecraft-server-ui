package loaderutil

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"io"
	"path/filepath"
	"strings"
)

type LanguageArtifact struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Source  string `json:"source"`
}
type LanguageEvidence struct {
	Selection  domain.VersionSelection     `json:"selection"`
	Generation string                      `json:"generation"`
	RuntimeID  string                      `json:"runtime_id"`
	JavaMajor  int                         `json:"java_major"`
	Languages  map[string]LanguageArtifact `json:"languages"`
}

// VerifiedLanguageEvidence reuses the complete offline generation verifier before
// interpreting only exact Forge language-library paths recorded by that receipt.
// NeoForge versions without separately versioned provider metadata stay unknown.
func VerifiedLanguageEvidence(ctx context.Context, dataDir, id string, spec domain.LaunchSpec) (LanguageEvidence, error) {
	out := LanguageEvidence{Languages: map[string]LanguageArtifact{}}
	if e := VerifyLaunch(ctx, dataDir, id, spec); e != nil {
		return out, e
	}
	paths, e := instance.NewPaths(dataDir)
	if e != nil {
		return out, e
	}
	base, e := paths.Resolve(id, "server", ".manager/loaders")
	if e != nil {
		return out, e
	}
	prefix := filepath.ToSlash(base) + "/"
	generation := ""
	for _, arg := range spec.Args {
		at := strings.Index(filepath.ToSlash(arg), prefix)
		if at >= 0 {
			rest := filepath.ToSlash(arg)[at+len(prefix):]
			if len(rest) >= 65 && rest[64] == '/' {
				generation = rest[:64]
				break
			}
		}
	}
	if generation == "" {
		return out, errors.New("verified loader generation unavailable")
	}
	root, e := safepath.OpenRoot(ctx, filepath.Join(base, generation))
	if e != nil {
		return out, e
	}
	defer root.Close()
	raw, e := ReadFile(root, "manifest.json", 16<<20)
	if e != nil {
		return out, e
	}
	var receipt receipt
	if e = json.Unmarshal(raw, &receipt); e != nil {
		return out, e
	}
	out.Selection = receipt.Plan.Selection
	out.Generation = generation
	out.RuntimeID = receipt.Runtime.ID
	out.JavaMajor = receipt.Runtime.Major
	if receipt.Plan.Selection.Flavor == domain.FlavorNeoForge {
		// This historical tuple uses package Implementation-Version. Newer FML
		// branches use module rawVersion; no distribution-version extrapolation.
		if receipt.Plan.Selection.MinecraftVersion != "1.20.4" || receipt.Plan.Selection.LoaderVersion != "20.4.253" {
			return out, nil
		}
		expected := "libraries/net/neoforged/fancymodloader/loader/2.0.17/loader-2.0.17.jar"
		for _, record := range receipt.Files {
			if record.Path != expected {
				continue
			}
			if record.SHA256 != neo204253LanguageSHA256 {
				return out, errors.New("historical Neo language artifact differs from reviewed exact bytes")
			}
			raw, e := ReadFile(root, expected, 1<<20)
			if e != nil {
				return out, e
			}
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != neo204253LanguageSHA256 {
				return out, errors.New("historical Neo language bytes changed")
			}
			for _, name := range []string{"javafml", "lowcodefml"} {
				version, e := languageManifest(raw, name, "net.neoforged", "IModLanguageProvider")
				if e != nil {
					return out, e
				}
				if version != "2.0" {
					return out, errors.New("historical Neo language metadata differs")
				}
				out.Languages[name] = LanguageArtifact{Version: version, Path: expected, SHA256: record.SHA256, Source: "verified-pinned-neoforge-language-manifest"}
			}
		}
		return out, nil
	}
	if receipt.Plan.Selection.Flavor != domain.FlavorForge {
		return out, nil
	}
	version := receipt.Plan.Selection.MinecraftVersion + "-" + receipt.Plan.Selection.LoaderVersion
	for name, artifact := range map[string]string{"javafml": "javafmllanguage", "lowcodefml": "lowcodelanguage"} {
		expected := "libraries/net/minecraftforge/" + artifact + "/" + version + "/" + artifact + "-" + version + ".jar"
		for _, record := range receipt.Files {
			if record.Path != expected {
				continue
			}
			raw, e := ReadFile(root, expected, 32<<20)
			if e != nil {
				return out, e
			}
			provider, e := forgeLanguageManifest(raw, name)
			if e != nil {
				return out, e
			}
			out.Languages[name] = LanguageArtifact{Version: provider, Path: expected, SHA256: record.SHA256, Source: "verified-loader-library-manifest"}
		}
	}
	return out, nil
}

const neo204253LanguageSHA256 = "fdb7c4ffd3b03ce5dba32d453a5335630416b4c820ce54eada5cba96e36be625"

func forgeLanguageManifest(raw []byte, name string) (string, error) {
	return languageManifest(raw, name, "net.minecraftforge", "IModLanguageProvider")
}
func languageManifest(raw []byte, name, namespace, serviceType string) (string, error) {
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil || len(z.File) > 10000 {
		return "", errors.New("invalid verified language archive")
	}
	manifest := ""
	service := false
	for _, f := range z.File {
		if f.Name != "META-INF/MANIFEST.MF" && f.Name != "META-INF/services/"+namespace+".neoforgespi.language."+serviceType && f.Name != "META-INF/services/"+namespace+".forgespi.language."+serviceType {
			continue
		}
		if f.UncompressedSize64 > 64<<10 {
			return "", errors.New("language metadata exceeds bound")
		}
		r, e := f.Open()
		if e != nil {
			return "", e
		}
		b, e := io.ReadAll(io.LimitReader(r, 64<<10+1))
		r.Close()
		if e != nil || len(b) > 64<<10 {
			return "", errors.New("language metadata unreadable")
		}
		if f.Name == "META-INF/MANIFEST.MF" {
			manifest = string(b)
		} else {
			expected := namespace + ".fml.javafmlmod.FMLJavaModLanguageProvider"
			if name == "lowcodefml" {
				expected = namespace + ".fml.lowcodemod.LowCodeModLanguageProvider"
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.TrimSpace(line) == expected {
					service = true
				}
			}
		}
	}
	if !service {
		return "", errors.New("verified language provider service unavailable")
	}
	props := manifestProperties([]byte(manifest))
	version := props["Implementation-Version"]
	if version == "" || len(version) > 128 || strings.ContainsAny(version, " \r\n\x00") {
		return "", errors.New("language implementation version unavailable")
	}
	return version, nil
}
