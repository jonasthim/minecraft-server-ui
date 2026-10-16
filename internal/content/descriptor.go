package content

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var scalar = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|(true|false))\s*(?:#.*)?$`)

func inspectJar(b []byte) (Descriptor, error) {
	return inspectArchive(bytes.NewReader(b), int64(len(b)))
}
func inspectArchive(reader io.ReaderAt, size int64) (Descriptor, error) {
	if e := boundedZipDirectory(reader, size); e != nil {
		return Descriptor{}, e
	}
	z, e := zip.NewReader(reader, size)
	if e != nil {
		return Descriptor{}, invalid("content must be a valid inert JAR archive")
	}
	if len(z.File) > 10000 {
		return Descriptor{}, invalid("JAR entry count exceeds limit")
	}
	manifestVersion := ""
	for _, entry := range z.File {
		if entry.Name != "META-INF/MANIFEST.MF" {
			continue
		}
		if entry.UncompressedSize64 > 64<<10 {
			return Descriptor{}, invalid("oversized JAR manifest")
		}
		f, e := entry.Open()
		if e != nil {
			return Descriptor{}, invalid("unreadable JAR manifest")
		}
		raw, e := io.ReadAll(io.LimitReader(f, (64<<10)+1))
		f.Close()
		if e != nil || len(raw) > 64<<10 {
			return Descriptor{}, invalid("invalid JAR manifest")
		}
		unfolded := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n ", ""), "\n ", "")
		for _, line := range strings.Split(unfolded, "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if ok && strings.EqualFold(key, "Implementation-Version") {
				manifestVersion = strings.TrimSpace(value)
			}
		}
	}
	d := Descriptor{Unknown: true, Required: map[string]string{}, Conflicts: map[string]string{}}
	seen := map[string]bool{}
	descriptors := 0
	for _, f := range z.File {
		n := strings.TrimSuffix(f.Name, "/")
		if !relative(n) || f.Mode()&os.ModeSymlink != 0 || !f.Mode().IsRegular() && !f.FileInfo().IsDir() {
			return d, invalid("unsafe JAR entry")
		}
		key := strings.ToLower(n)
		if seen[key] {
			return d, invalid("duplicate JAR entries")
		}
		seen[key] = true
		if f.Name != "fabric.mod.json" && f.Name != "plugin.yml" && f.Name != "paper-plugin.yml" && f.Name != "META-INF/mods.toml" && f.Name != "META-INF/neoforge.mods.toml" {
			continue
		}
		descriptors++
		if descriptors > 1 {
			return d, invalid("ambiguous multiple content descriptors")
		}
		if f.UncompressedSize64 > 256<<10 {
			return d, invalid("oversized content descriptor")
		}
		r, e := f.Open()
		if e != nil {
			return d, invalid("unreadable descriptor")
		}
		raw, e := io.ReadAll(io.LimitReader(r, (256<<10)+1))
		r.Close()
		if e != nil || len(raw) > 256<<10 {
			return d, invalid("descriptor exceeds limits")
		}
		switch f.Name {
		case "fabric.mod.json":
			var v struct {
				ID, Version, Environment string
				Depends                  map[string]json.RawMessage
				Breaks                   map[string]json.RawMessage
			}
			if json.Unmarshal(raw, &v) != nil || !portable(v.ID) || v.Version == "" {
				return d, invalid("invalid Fabric descriptor")
			}
			d.ID = v.ID
			d.Version = v.Version
			d.Loader = "fabric"
			d.ClientOnly = v.Environment == "client"
			if v.Environment != "" && v.Environment != "*" && v.Environment != "server" && v.Environment != "client" {
				return d, invalid("unknown Fabric environment")
			}
			for k, v := range v.Depends {
				var s string
				if json.Unmarshal(v, &s) != nil {
					return d, invalid("complex Fabric dependency needs explicit review")
				}
				d.Required[k] = s
			}
			for k, v := range v.Breaks {
				var s string
				if json.Unmarshal(v, &s) != nil {
					return d, invalid("complex Fabric conflict needs explicit review")
				}
				d.Conflicts[k] = s
			}
			d.Minecraft = d.Required["minecraft"]
			d.LoaderRequirement = d.Required["fabricloader"]
			delete(d.Required, "minecraft")
			delete(d.Required, "fabricloader")
			delete(d.Required, "java")
			d.Unknown = false
		case "plugin.yml", "paper-plugin.yml":
			var v struct {
				Name, Version string
				APIVersion    string `yaml:"api-version"`
				Depend        []string
				SoftDepend    []string `yaml:"softdepend"`
				Dependencies  map[string]any
			}
			dec := yaml.NewDecoder(bytes.NewReader(raw))
			if dec.Decode(&v) != nil || !portable(v.Name) || v.Version == "" {
				return d, invalid("invalid plugin descriptor")
			}
			if len(v.Dependencies) > 0 {
				return d, invalid("Paper dependency structures require explicit review")
			}
			d.ID = strings.ToLower(v.Name)
			d.Version = v.Version
			d.Loader = "paper"
			d.Minecraft = v.APIVersion
			for _, n := range v.Depend {
				d.Required[strings.ToLower(n)] = "*"
			}
			d.Unknown = false
		default:
			d.Loader = "forge"
			if strings.Contains(f.Name, "neoforge") {
				d.Loader = "neoforge"
			}
			fmlRequirement := ""
			var section string
			fields := map[string]string{}
			mods := 0
			flush := func() error {
				if section == "mods" {
					mods++
					if mods > 1 {
						return invalid("multi-mod JAR requires explicit review")
					}
					d.ID = fields["modId"]
					d.Version = fields["version"]
				}
				if strings.HasPrefix(section, "dependencies.") {
					required := fields["mandatory"] == "true" || fields["type"] == "required"
					if fields["side"] == "CLIENT" && required {
						d.ClientOnly = true
					}
					id := fields["modId"]
					if required {
						switch id {
						case "minecraft":
							d.Minecraft = fields["versionRange"]
						case "forge", "neoforge":
							d.LoaderRequirement = fields["versionRange"]
						default:
							d.Required[id] = fields["versionRange"]
						}
					}
					if fields["type"] == "incompatible" {
						d.Conflicts[id] = fields["versionRange"]
					}
				}
				return nil
			}
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
					if e := flush(); e != nil {
						return d, e
					}
					section = strings.Trim(line, "[]")
					fields = map[string]string{}
					continue
				}
				if strings.HasPrefix(line, "[") {
					if e := flush(); e != nil {
						return d, e
					}
					section = ""
					fields = map[string]string{}
					continue
				}
				m := scalar.FindStringSubmatch(line)
				if m != nil {
					value := m[2] + m[3] + m[4]
					fields[m[1]] = value
					if section == "" && m[1] == "loaderVersion" {
						fmlRequirement = value
					}
					if section == "" && m[1] == "modLoader" && value != "javafml" && value != "lowcodefml" {
						return d, invalid("unsupported Forge mod loader")
					}
				} else if strings.Contains(line, "mandatory") || strings.Contains(line, "modId") || strings.Contains(line, "versionRange") || strings.Contains(line, "type") {
					return d, invalid("complex Forge descriptor requires explicit review")
				}
			}
			if e := flush(); e != nil {
				return d, e
			}
			if d.Version == "${file.jarVersion}" {
				d.Version = manifestVersion
			}
			if !portable(d.ID) || d.Version == "" || strings.Contains(d.Version, "${") {
				return d, invalid("unresolved Forge identity or version")
			}
			d.Unknown = fmlRequirement != "" && fmlRequirement != "*"
		}
	}
	if len(d.Required) > 128 || len(d.Conflicts) > 128 {
		return d, invalid("descriptor dependency limit exceeded")
	}
	return d, nil
}
func numeric(v string) ([]int, bool) {
	v = strings.TrimPrefix(v, "v")
	p := strings.Split(v, ".")
	out := []int{}
	for _, s := range p {
		n, e := strconv.Atoi(s)
		if e != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, len(out) > 0
}
func compare(a, b string) (int, bool) {
	x, ok := numeric(a)
	if !ok {
		return 0, false
	}
	y, ok := numeric(b)
	if !ok {
		return 0, false
	}
	n := len(x)
	if len(y) > n {
		n = len(y)
	}
	for i := 0; i < n; i++ {
		v, w := 0, 0
		if i < len(x) {
			v = x[i]
		}
		if i < len(y) {
			w = y[i]
		}
		if v < w {
			return -1, true
		}
		if v > w {
			return 1, true
		}
	}
	return 0, true
}

// Deliberately refuses expressions that this bounded checker cannot prove.
func satisfies(version, requirement string) bool {
	requirement = strings.TrimSpace(requirement)
	if requirement == "" {
		return false
	}
	if requirement == "*" {
		return true
	}
	if requirement == version || requirement == "="+version {
		return true
	}
	if strings.Contains(requirement, "||") {
		for _, v := range strings.Split(requirement, "||") {
			if satisfies(version, v) {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(requirement, "[") || strings.HasPrefix(requirement, "(") {
		if len(requirement) < 3 {
			return false
		}
		s := requirement[1 : len(requirement)-1]
		parts := strings.Split(s, ",")
		if len(parts) == 1 {
			return requirement[0] == '[' && requirement[len(requirement)-1] == ']' && parts[0] == version
		}
		if len(parts) != 2 {
			return false
		}
		low, high := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if low != "" {
			n, ok := compare(version, low)
			if !ok || n < 0 || n == 0 && requirement[0] != '[' {
				return false
			}
		}
		if high != "" {
			n, ok := compare(version, high)
			if !ok || n > 0 || n == 0 && requirement[len(requirement)-1] != ']' {
				return false
			}
		}
		return true
	}
	terms := strings.Fields(requirement)
	if len(terms) > 1 {
		for _, p := range terms {
			if !satisfies(version, p) {
				return false
			}
		}
		return true
	}
	for _, op := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(requirement, op) {
			n, ok := compare(version, strings.TrimSpace(strings.TrimPrefix(requirement, op)))
			if !ok {
				return false
			}
			switch op {
			case ">=":
				return n >= 0
			case "<=":
				return n <= 0
			case ">":
				return n > 0
			case "<":
				return n < 0
			case "=":
				return n == 0
			}
		}
	}
	if strings.HasSuffix(requirement, ".*") {
		return strings.HasPrefix(version, strings.TrimSuffix(requirement, "*"))
	}
	return false
}
func descriptorBlockers(d Descriptor, t Tuple, kind catalog.Kind, items []Installed) []string {
	b := []string{}
	if d.ClientOnly {
		b = append(b, "Client-only content is refused.")
	}
	if d.Unknown {
		b = append(b, "Descriptor compatibility is unknown; administrator trial required.")
	}
	if d.Loader != "" && d.Loader != t.Loader {
		b = append(b, "Descriptor loader does not match the installed server type.")
	}
	mc := satisfies(t.Minecraft, d.Minecraft)
	if kind == catalog.Plugin && d.Minecraft != "" {
		mc = t.Minecraft == d.Minecraft || strings.HasPrefix(t.Minecraft, d.Minecraft+".")
	}
	if !mc {
		b = append(b, "Descriptor Minecraft/API requirement is unresolved or incompatible.")
	}
	if d.LoaderRequirement != "" && !satisfies(t.LoaderVersion, d.LoaderRequirement) {
		b = append(b, "Descriptor loader version requirement is incompatible.")
	}
	for id, req := range d.Required {
		found := false
		for _, i := range items {
			if i.Enabled && i.Descriptor.ID == id && satisfies(i.Descriptor.Version, req) {
				found = true
			}
		}
		if !found {
			b = append(b, "Unresolved required descriptor dependency: "+id)
		}
	}
	for id, req := range d.Conflicts {
		for _, i := range items {
			if i.Enabled && i.Descriptor.ID == id && satisfies(i.Descriptor.Version, req) {
				b = append(b, "Installed descriptor incompatibility: "+id)
			}
		}
	}
	return b
}

// Bound central-directory allocation before archive/zip parses untrusted headers.
// A <=2GiB JAR with <=10000 entries does not require ZIP64 directory records.
func boundedZipDirectory(r io.ReaderAt, size int64) error {
	if size < 22 || size > MaxArtifactBytes {
		return invalid("JAR size outside limits")
	}
	n := int64(65557)
	if size < n {
		n = size
	}
	tail := make([]byte, n)
	if _, e := r.ReadAt(tail, size-n); e != nil {
		return invalid("JAR end record unreadable")
	}
	for i := len(tail) - 22; i >= 0; i-- {
		if !bytes.Equal(tail[i:i+4], []byte{'P', 'K', 5, 6}) {
			continue
		}
		end := tail[i:]
		if int(binary.LittleEndian.Uint16(end[20:22])) != len(end)-22 {
			continue
		}
		count := binary.LittleEndian.Uint16(end[10:12])
		directorySize := binary.LittleEndian.Uint32(end[12:16])
		offset := binary.LittleEndian.Uint32(end[16:20])
		if binary.LittleEndian.Uint16(end[4:6]) != 0 || binary.LittleEndian.Uint16(end[6:8]) != 0 || binary.LittleEndian.Uint16(end[8:10]) != count || count > 10000 || directorySize > 4<<20 || uint64(offset)+uint64(directorySize) > uint64(size-n+int64(i)) {
			return invalid("JAR central directory exceeds bounded single-volume limits")
		}
		return nil
	}
	return invalid("JAR end record missing")
}
