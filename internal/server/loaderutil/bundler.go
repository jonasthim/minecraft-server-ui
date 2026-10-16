package loaderutil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sort"
	"strings"
)

// PrepareMinecraft expands only the publisher-listed nested JARs, verifying each
// published SHA256 from the hash-verified outer Mojang artifact. The bundler is
// never executed, and game launch never needs to populate the immutable release.
func PrepareMinecraft(ctx context.Context, root *os.Root, reuse bool) (string, []string, error) {
	b, e := ReadFile(root, "server.jar", MaxArtifact)
	if e != nil {
		return "", nil, e
	}
	z, e := checkedZip(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		return "", nil, e
	}
	versions, e := zipBytes(z, "META-INF/versions.list", 1<<20)
	if os.IsNotExist(e) {
		return "server.jar", []string{"server.jar"}, nil
	}
	if e != nil {
		return "", nil, e
	}
	libraries, e := zipBytes(z, "META-INF/libraries.list", 1<<20)
	if e != nil {
		return "", nil, errors.New("bundled server library list missing")
	}
	cp := []string{}
	main := ""
	for group, data := range map[string][]byte{"versions": versions, "libraries": libraries} {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if e := ctx.Err(); e != nil {
				return "", nil, e
			}
			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) != 3 || !ValidDigest("sha256", parts[0]) || SafePath(parts[2]) != nil {
				return "", nil, errors.New("invalid bundled server entry")
			}
			if group == "versions" {
				if main != "" {
					return "", nil, errors.New("bundled server has multiple game versions")
				}
				main = "minecraft/versions/" + parts[2]
			}
			source := "META-INF/" + group + "/" + parts[2]
			dest := "minecraft/" + group + "/" + parts[2]
			nested, e := zipBytes(z, source, MaxArtifact)
			if e != nil || Digest("sha256", nested) != parts[0] {
				return "", nil, errors.New("bundled server entry hash mismatch")
			}
			if e = ValidateJar(bytes.NewReader(nested), int64(len(nested))); e != nil {
				return "", nil, e
			}
			if !reuse {
				if e = WriteFile(root, dest, nested); e != nil {
					return "", nil, e
				}
			} else {
				actual, e := ReadFile(root, dest, MaxArtifact)
				if e != nil || Digest("sha256", actual) != parts[0] {
					return "", nil, errors.New("installed bundled server entry changed")
				}
			}
			cp = append(cp, dest)
		}
	}
	if main == "" {
		return "", nil, errors.New("bundled server game entry missing")
	}
	sort.Strings(cp)
	return main, cp, nil
}
