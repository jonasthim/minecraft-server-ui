package loaderutil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
)

// VerifyGenerated independently checks every server output hash published by the
// verified installer profile, including data hashes not repeated on processors.
func VerifyGenerated(ctx context.Context, root *os.Root, p Profile) error {
	resolve := func(value string) (string, error) {
		if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
			key := value[1 : len(value)-1]
			v, ok := p.Data[key]
			if !ok || v.Server == "" {
				return "", errors.New("unresolved installer output variable")
			}
			value = v.Server
		}
		return strings.Trim(value, "'"), nil
	}
	outputs := map[string]string{}
	add := func(path, checksum string) error {
		value, e := resolve(path)
		if e != nil {
			return e
		}
		sum, e := resolve(checksum)
		if e != nil || !ValidDigest("sha1", sum) {
			return errors.New("invalid published processor output digest")
		}
		if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
			return errors.New("unreviewed processor output path")
		}
		name, e := MavenPath(value[1 : len(value)-1])
		if e != nil {
			return e
		}
		name = "libraries/" + name
		if prior, ok := outputs[name]; ok && prior != sum {
			return errors.New("conflicting processor output hashes")
		}
		outputs[name] = sum
		return nil
	}
	for key, v := range p.Data {
		if !strings.HasSuffix(key, "_SHA") || v.Server == "" {
			continue
		}
		base := strings.TrimSuffix(key, "_SHA")
		if target, ok := p.Data[base]; ok && target.Server != "" {
			if e := add("{"+base+"}", v.Server); e != nil {
				return e
			}
		}
	}
	for _, processor := range p.Processors {
		server := len(processor.Sides) == 0
		for _, side := range processor.Sides {
			if side == "server" {
				server = true
			}
		}
		if !server {
			continue
		}
		for path, sum := range processor.Outputs {
			if e := add(path, sum); e != nil {
				return e
			}
		}
	}
	for name, sum := range outputs {
		if e := ctx.Err(); e != nil {
			return e
		}
		b, e := ReadFile(root, name, MaxArtifact)
		if e != nil || Digest("sha1", b) != sum {
			return errors.New("generated loader output differs from publisher checksum")
		}
	}
	return nil
}
func VerifyBundledLibraries(ctx context.Context, root *os.Root, server string) error {
	b, e := ReadFile(root, server, MaxArtifact)
	if e != nil {
		return e
	}
	z, e := checkedZip(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		return e
	}
	data, e := zipBytes(z, "META-INF/libraries.list", 1<<20)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(parts) != 3 || !ValidDigest("sha256", parts[0]) || SafePath(parts[2]) != nil {
			return errors.New("invalid Minecraft bundled library reference")
		}
		b, e := ReadFile(root, "libraries/"+parts[2], MaxArtifact)
		if e != nil || Digest("sha256", b) != parts[0] {
			return errors.New("installed Minecraft library differs from publisher bundled hash")
		}
		if e := ctx.Err(); e != nil {
			return e
		}
	}
	return nil
}
