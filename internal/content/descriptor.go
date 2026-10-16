package content

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"strconv"
	"strings"
)

func inspectJar(b []byte) (Descriptor, error) {
	return inspectArchive(bytes.NewReader(b), int64(len(b)))
}

type nestedBudget struct {
	bytes      int64
	jars       int
	identities int
}

func inspectArchive(reader io.ReaderAt, size int64) (Descriptor, error) {
	return inspectArchiveTree(reader, size, 0, &nestedBudget{})
}
func inspectArchiveTree(reader io.ReaderAt, size int64, depth int, budget *nestedBudget) (Descriptor, error) {
	if depth > 4 || budget.jars >= 2048 {
		return Descriptor{}, invalid("nested JAR graph exceeds depth/item bound")
	}
	budget.jars++
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
				Recommends               map[string]json.RawMessage
				Conflicts                map[string]json.RawMessage
				Provides                 []string
				Jars                     []struct {
					File string `json:"file"`
				}
				Breaks map[string]json.RawMessage
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
					var alternatives []string
					if json.Unmarshal(v, &alternatives) != nil || len(alternatives) == 0 || len(alternatives) > 64 {
						return d, invalid("unsupported Fabric dependency expression")
					}
					s = strings.Join(alternatives, " || ")
				}
				d.Required[k] = s
			}
			for k, v := range v.Breaks {
				var s string
				if json.Unmarshal(v, &s) != nil {
					var alternatives []string
					if json.Unmarshal(v, &alternatives) != nil || len(alternatives) == 0 || len(alternatives) > 64 {
						return d, invalid("unsupported Fabric conflict expression")
					}
					s = strings.Join(alternatives, " || ")
				}
				d.Conflicts[k] = s
			}
			d.Optional = map[string]string{}
			for id, expression := range v.Recommends {
				var requirement string
				if json.Unmarshal(expression, &requirement) != nil {
					var alternatives []string
					if json.Unmarshal(expression, &alternatives) != nil || len(alternatives) == 0 || len(alternatives) > 64 {
						return d, invalid("unsupported Fabric recommendation")
					}
					requirement = strings.Join(alternatives, " || ")
				}
				d.Optional[id] = requirement
			}
			for id, expression := range v.Conflicts {
				var requirement string
				if json.Unmarshal(expression, &requirement) != nil {
					var alternatives []string
					if json.Unmarshal(expression, &alternatives) != nil || len(alternatives) == 0 || len(alternatives) > 64 {
						return d, invalid("unsupported Fabric soft conflict")
					}
					requirement = strings.Join(alternatives, " || ")
				}
				if previous := d.Conflicts[id]; previous != "" {
					requirement = previous + " || " + requirement
				}
				d.Conflicts[id] = requirement
			}
			d.Minecraft = d.Required["minecraft"]
			d.LoaderRequirement = d.Required["fabricloader"]
			delete(d.Required, "minecraft")
			delete(d.Required, "fabricloader")
			d.JavaRequirement = d.Required["java"]
			delete(d.Required, "java")
			for _, alias := range v.Provides {
				if !portable(alias) {
					return d, invalid("invalid Fabric provided identity")
				}
				d.Identities = append(d.Identities, ModIdentity{ID: alias, Version: d.Version})
			}
			d.Unknown = false
			nestedSeen := map[string]bool{}
			for _, nested := range v.Jars {
				if !relative(nested.File) || !strings.HasSuffix(strings.ToLower(nested.File), ".jar") || nestedSeen[strings.ToLower(nested.File)] {
					return d, invalid("unsafe or duplicate nested JAR declaration")
				}
				nestedSeen[strings.ToLower(nested.File)] = true
				found := false
				for _, entry := range z.File {
					if entry.Name != nested.File {
						continue
					}
					found = true
					if entry.UncompressedSize64 > 32<<20 || entry.UncompressedSize64 > uint64((128<<20)-budget.bytes) || entry.UncompressedSize64 > 1<<20 && (entry.CompressedSize64 == 0 || entry.UncompressedSize64/entry.CompressedSize64 > 200) {
						return d, invalid("nested JAR amplification/aggregate byte bound exceeded")
					}
					budget.bytes += int64(entry.UncompressedSize64)
					r, e := entry.Open()
					if e != nil {
						return d, e
					}
					b, e := io.ReadAll(io.LimitReader(r, 32<<20+1))
					r.Close()
					if e != nil || len(b) > 32<<20 {
						return d, invalid("nested JAR read exceeds bound")
					}
					child, e := inspectArchiveTree(bytes.NewReader(b), int64(len(b)), depth+1, budget)
					if e != nil {
						return d, e
					}
					if child.ClientOnly {
						continue
					}
					if child.Loader != "fabric" || child.Unknown {
						d.Unknown = true
					}
					budget.identities += 1 + len(child.Identities)
					if budget.identities > 2048 {
						return d, invalid("nested identity bound exceeded")
					}
					d.Identities = append(d.Identities, ModIdentity{ID: child.ID, Version: child.Version, Required: child.Required, Optional: child.Optional, Conflicts: child.Conflicts})
					d.Identities = append(d.Identities, child.Identities...)
					if child.Minecraft != "" {
						d.Minecraft = joinRequirement(d.Minecraft, child.Minecraft)
					}
					if child.LoaderRequirement != "" {
						d.LoaderRequirement = joinRequirement(d.LoaderRequirement, child.LoaderRequirement)
					}
					if child.JavaRequirement != "" {
						d.JavaRequirement = joinRequirement(d.JavaRequirement, child.JavaRequirement)
					}
				}
				if !found {
					return d, invalid("declared nested JAR is missing")
				}
			}
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
			d, e = inspectForgeTOML(raw, strings.Contains(f.Name, "neoforge"), manifestVersion)
			if e != nil {
				return d, e
			}
		}
	}
	if d.Loader == "forge" || d.Loader == "neoforge" {
		for _, entry := range z.File {
			if entry.Name == "META-INF/jarjar/metadata.json" {
				d.Unknown = true
			}
		}
	}
	if len(d.Required) > 2048 || len(d.Conflicts) > 2048 {
		return d, invalid("descriptor dependency limit exceeded")
	}
	return d, nil
}
func numeric(v string) ([]int, bool) {
	v = strings.TrimPrefix(v, "v")
	v = strings.SplitN(v, "+", 2)[0]
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
	if strings.Contains(requirement, " && ") {
		for _, term := range strings.Split(requirement, " && ") {
			if !satisfies(version, term) {
				return false
			}
		}
		return true
	}
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

// TOML decoding is bounded by the enclosing 256 KiB descriptor read. All
// compatibility-bearing fields are typed; invalid scalars/duplicate TOML keys
// fail decoding instead of silently disappearing in a partial line parser.
func inspectForgeTOML(raw []byte, neo bool, manifestVersion string) (Descriptor, error) {
	type dependency struct {
		ModID        string `toml:"modId"`
		Mandatory    *bool  `toml:"mandatory"`
		Type         string `toml:"type"`
		VersionRange string `toml:"versionRange"`
		Ordering     string `toml:"ordering"`
		Side         string `toml:"side"`
	}
	var v struct {
		ModLoader      string `toml:"modLoader"`
		LoaderVersion  string `toml:"loaderVersion"`
		ClientSideOnly bool   `toml:"clientSideOnly"`
		Mods           []struct {
			ModID    string            `toml:"modId"`
			Version  string            `toml:"version"`
			Features map[string]string `toml:"features"`
		} `toml:"mods"`
		Dependencies map[string][]dependency `toml:"dependencies"`
		Properties   map[string]string       `toml:"properties"`
	}
	d := Descriptor{Loader: "forge", Required: map[string]string{}, Optional: map[string]string{}, Conflicts: map[string]string{}}
	if neo {
		d.Loader = "neoforge"
	}
	if e := toml.Unmarshal(raw, &v); e != nil {
		return d, invalid("invalid typed Forge TOML descriptor")
	}
	historicalNeo := false
	if !neo {
		for _, dependencies := range v.Dependencies {
			for _, dependency := range dependencies {
				if dependency.ModID == "neoforge" {
					neo = true
					historicalNeo = true
					d.Loader = "neoforge"
				}
			}
		}
	}
	if neo && v.LoaderVersion == "" {
		v.LoaderVersion = "[1,)"
	}
	if (v.ModLoader != "javafml" && v.ModLoader != "lowcodefml") || v.LoaderVersion == "" || len(v.Mods) == 0 || len(v.Mods) > 2048 {
		return d, invalid("unsupported Forge language or mod list")
	}
	d.LanguageLoader = v.ModLoader
	d.LanguageRequirement = v.LoaderVersion
	d.ClientOnly = v.ClientSideOnly
	ids := map[string]bool{}
	for _, mod := range v.Mods {
		version := mod.Version
		if version == "" {
			version = "1"
		}
		if version == "${file.jarVersion}" {
			version = manifestVersion
		}
		for key, value := range v.Properties {
			version = strings.ReplaceAll(version, "${file."+key+"}", value)
		}
		if !portable(mod.ModID) || version == "" || strings.Contains(version, "${") || ids[mod.ModID] {
			return d, invalid("unresolved or duplicate Forge identity")
		}
		ids[mod.ModID] = true
		identity := ModIdentity{ID: mod.ModID, Version: version, Required: map[string]string{}, Optional: map[string]string{}, Conflicts: map[string]string{}}
		for feature, requirement := range mod.Features {
			if feature == "java_version" {
				d.JavaRequirement = joinRequirement(d.JavaRequirement, requirement)
			} else {
				d.Unknown = true
			}
		}
		for _, dep := range v.Dependencies[mod.ModID] {
			if dep.Side != "" && dep.Side != "BOTH" && dep.Side != "SERVER" && dep.Side != "CLIENT" {
				return d, invalid("unknown dependency side")
			}
			if dep.Side == "CLIENT" {
				continue
			}
			if !portable(dep.ModID) {
				return d, invalid("invalid Forge dependency identity")
			}
			req := dep.VersionRange
			if req == "" {
				req = "*"
			}
			kind := dep.Type
			if neo {
				if kind == "" {
					kind = "required"
					if historicalNeo {
						if dep.Mandatory == nil {
							return d, invalid("historical NeoForge mandatory flag missing")
						}
						if !*dep.Mandatory {
							kind = "optional"
						}
					}
				}
				switch kind {
				case "required", "optional", "incompatible", "discouraged":
				default:
					return d, invalid("unknown NeoForge dependency type")
				}
			} else {
				if kind != "" {
					return d, invalid("unsupported Forge dependency type")
				}
				if dep.Mandatory == nil {
					return d, invalid("Forge dependency mandatory flag missing")
				}
				kind = "optional"
				if *dep.Mandatory {
					kind = "required"
				}
			}
			if dep.Ordering != "" && dep.Ordering != "NONE" {
				if dep.Ordering != "BEFORE" && dep.Ordering != "AFTER" {
					return d, invalid("unknown dependency ordering")
				}
				d.Ordering = append(d.Ordering, DependencyOrdering{Owner: mod.ModID, Target: dep.ModID, Direction: dep.Ordering})
			}
			switch dep.ModID {
			case "minecraft":
				if kind == "required" || kind == "optional" {
					d.Minecraft = joinRequirement(d.Minecraft, req)
				} else {
					return d, invalid("unsupported Minecraft builtin incompatibility constraint")
				}
			case "forge", "neoforge":
				if dep.ModID != d.Loader {
					return d, invalid("other-loader builtin constraint cannot be established")
				}
				if kind == "required" || kind == "optional" {
					d.LoaderRequirement = joinRequirement(d.LoaderRequirement, req)
				} else {
					return d, invalid("unsupported loader builtin incompatibility constraint")
				}
			default:
				switch kind {
				case "required":
					identity.Required[dep.ModID] = joinRequirement(identity.Required[dep.ModID], req)
				case "optional":
					identity.Optional[dep.ModID] = joinRequirement(identity.Optional[dep.ModID], req)
				case "incompatible", "discouraged":
					identity.Conflicts[dep.ModID] = joinConflict(identity.Conflicts[dep.ModID], req)
				}
			}
		}
		d.Identities = append(d.Identities, identity)
	}
	for id := range v.Dependencies {
		if !ids[id] {
			return d, invalid("dependency table belongs to absent mod identity")
		}
	}
	primary := d.Identities[0]
	d.ID = primary.ID
	d.Version = primary.Version
	d.Required = primary.Required
	d.Optional = primary.Optional
	d.Conflicts = primary.Conflicts
	d.Identities = d.Identities[1:]
	return d, nil
}
func joinRequirement(a, b string) string {
	if a == "" {
		return b
	}
	return a + " && " + b
}

func joinConflict(a, b string) string {
	if a == "" {
		return b
	}
	return a + " || " + b
}
