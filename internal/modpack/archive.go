package modpack

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

const MaxArchiveBytes int64 = 2 << 30
const MaxPackFiles = 8192
const MaxFileBytes int64 = 2 << 30
const MaxExpandedBytes uint64 = 8 << 30

type PackFile struct {
	Path         string            `json:"path"`
	Size         int64             `json:"size"`
	Hashes       map[string]string `json:"hashes"`
	Downloads    []string          `json:"downloads,omitempty"`
	Server       string            `json:"server"`
	Bytes        []byte            `json:"-"`
	ArchiveEntry string            `json:"archive_entry,omitempty"`
	ProjectID    string            `json:"project_id,omitempty"`
	FileID       string            `json:"file_id,omitempty"`
}
type Pack struct {
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	Minecraft     string              `json:"minecraft"`
	Loader        string              `json:"loader"`
	LoaderVersion string              `json:"loader_version"`
	Files         map[string]PackFile `json:"files"`
	Excluded      []string            `json:"excluded"`
	Blockers      []string            `json:"blockers"`
}

func packError(s string) error { return fmt.Errorf("pack: %s", s) }

// ParseArchive validates the complete directory before opening any entry. It
// never extracts to host paths. Embedded overrides are bounded config text;
// JAR entries remain indexed for streamed staging by the coordinator.
func ParseArchive(ctx context.Context, r io.ReaderAt, size int64, format string) (Pack, error) {
	p := Pack{Files: map[string]PackFile{}, Excluded: []string{}, Blockers: []string{}}
	if e := zipBounds(r, size); e != nil {
		return p, e
	}
	z, e := zip.NewReader(r, size)
	if e != nil {
		return p, packError("invalid ZIP")
	}
	seen := map[string]bool{}
	directory := map[string]bool{}
	var expanded uint64
	for _, f := range z.File {
		if e = ctx.Err(); e != nil {
			return p, e
		}
		name := strings.TrimSuffix(f.Name, "/")
		if !safeRelative(name) || f.Mode()&os.ModeSymlink != 0 || !f.Mode().IsRegular() && !f.FileInfo().IsDir() {
			return p, packError("unsafe archive entry")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return p, packError("duplicate or case-colliding archive entries")
		}
		seen[key] = true
		directory[key] = f.FileInfo().IsDir()
		if f.UncompressedSize64 > uint64(MaxFileBytes) || f.UncompressedSize64 > MaxExpandedBytes-expanded {
			return p, packError("expanded archive size exceeds bound")
		}
		expanded += f.UncompressedSize64
		if f.UncompressedSize64 > 1<<20 && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > 200) {
			return p, packError("archive compression ratio exceeds bound")
		}
	}
	for key := range seen {
		parts := strings.Split(key, "/")
		for i := 1; i < len(parts); i++ {
			prefix := strings.Join(parts[:i], "/")
			if seen[prefix] && !directory[prefix] {
				return p, packError("file-directory collision")
			}
		}
	}
	var configBytes int64

	if format == "modrinth" {
		var idx struct {
			FormatVersion         int `json:"formatVersion"`
			Game, Name, VersionID string
			Dependencies          map[string]string
			Files                 []struct {
				Path      string
				Hashes    map[string]string
				Downloads []string
				Env       map[string]string
				FileSize  int64 `json:"fileSize"`
			}
		}
		raw, e := zipRead(ctx, z, "modrinth.index.json", 4<<20)
		if e != nil {
			return p, e
		}
		if !utf8.Valid(raw) || json.Unmarshal(raw, &idx) != nil || idx.FormatVersion != 1 || idx.Game != "minecraft" || idx.Name == "" || idx.VersionID == "" || len(idx.Files) > MaxPackFiles {
			return p, packError("invalid mrpack index")
		}
		p.Name = idx.Name
		p.Version = idx.VersionID
		p.Minecraft = idx.Dependencies["minecraft"]
		for k, v := range idx.Dependencies {
			switch k {
			case "minecraft":
			case "fabric-loader", "forge", "neoforge":
				if p.Loader != "" {
					return p, packError("multiple pack loaders")
				}
				p.Loader = k
				if k == "fabric-loader" {
					p.Loader = "fabric"
				}
				p.LoaderVersion = v
			default:
				p.Blockers = append(p.Blockers, "Unsupported pack dependency: "+k)
			}
		}
		if p.Minecraft == "" || p.Loader == "" || p.LoaderVersion == "" {
			p.Blockers = append(p.Blockers, "Exact Minecraft/loader dependency is required")
		}
		destinations := map[string]bool{}
		for _, f := range idx.Files {
			if !safeRelative(f.Path) || destinations[strings.ToLower(f.Path)] || !validHex(f.Hashes["sha1"], 40) || !validHex(f.Hashes["sha512"], 128) || f.FileSize < 0 || f.FileSize > MaxFileBytes || len(f.Downloads) == 0 || len(f.Downloads) > 8 {
				return p, packError("invalid mrpack file/hash/path")
			}
			destinations[strings.ToLower(f.Path)] = true
			server := "required"
			if f.Env != nil {
				server = f.Env["server"]
				if server != "required" && server != "optional" && server != "unsupported" {
					return p, packError("unknown per-file server environment")
				}
			}
			if server == "unsupported" {
				p.Excluded = append(p.Excluded, f.Path)
				continue
			}
			if !allowedPath(f.Path) {
				return p, packError("pack file targets excluded server path")
			}
			p.Files[f.Path] = PackFile{Path: f.Path, Size: f.FileSize, Hashes: f.Hashes, Downloads: f.Downloads, Server: server}
		}
		for _, layer := range []string{"overrides/", "server-overrides/"} {
			for _, f := range z.File {
				if !strings.HasPrefix(f.Name, layer) || f.FileInfo().IsDir() {
					continue
				}
				dest := strings.TrimPrefix(f.Name, layer)
				if !allowedPath(dest) {
					return p, packError("override targets excluded server path")
				}
				var b []byte
				if !strings.HasSuffix(dest, ".jar") && !binaryResource(dest) {
					b, e = zipRead(ctx, z, f.Name, 1<<20)
					configBytes += int64(len(b))
					if configBytes > 32<<20 {
						return p, packError("aggregate override text exceeds 32 MiB")
					}
					if e != nil || !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
						return p, packError("override is not bounded UTF-8 config text")
					}
				}
				for existing := range p.Files {
					if strings.EqualFold(existing, dest) && existing != dest {
						return p, packError("case-colliding destination paths")
					}
				}
				p.Files[dest] = PackFile{Path: dest, Size: int64(f.UncompressedSize64), Server: "required", Bytes: b, ArchiveEntry: f.Name}
			}
		}
		for _, f := range z.File {
			if f.FileInfo().IsDir() || f.Name == "modrinth.index.json" || strings.HasPrefix(f.Name, "overrides/") || strings.HasPrefix(f.Name, "server-overrides/") {
				continue
			}
			if strings.HasPrefix(f.Name, "client-overrides/") {
				p.Excluded = append(p.Excluded, f.Name)
				continue
			}
			return p, packError("unrecognized mrpack archive root")
		}
	} else if format == "curseforge-server" {
		p.Name = "Official CurseForge server pack"
		p.Blockers = append(p.Blockers, "Server pack tuple must be bound to official exact catalog metadata")
		for _, f := range z.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if !allowedPath(f.Name) {
				p.Excluded = append(p.Excluded, f.Name)
				continue
			}
			var b []byte
			if !strings.HasSuffix(f.Name, ".jar") && !binaryResource(f.Name) {
				b, e = zipRead(ctx, z, f.Name, 1<<20)
				configBytes += int64(len(b))
				if configBytes > 32<<20 {
					return p, packError("aggregate override text exceeds 32 MiB")
				}
				if e != nil || !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
					return p, packError("server override is not bounded config text")
				}
			}
			p.Files[f.Name] = PackFile{Path: f.Name, Size: int64(f.UncompressedSize64), Server: "required", Bytes: b, ArchiveEntry: f.Name}
		}
		if len(p.Files) == 0 {
			return p, packError("server pack has no recognized server content")
		}
	} else {
		return p, packError("unsupported pack format")
	}
	if len(p.Files) > MaxPackFiles {
		return p, packError("combined pack file count exceeds bound")
	}
	for name := range p.Files {
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			if _, ok := p.Files[strings.Join(parts[:i], "/")]; ok {
				return p, packError("destination file-directory collision")
			}
		}
	}
	sort.Strings(p.Excluded)
	sort.Strings(p.Blockers)
	return p, nil
}
func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}
func zipRead(ctx context.Context, z *zip.Reader, name string, max int64) ([]byte, error) {
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		if f.UncompressedSize64 > uint64(max) {
			return nil, packError("archive metadata/config exceeds bound")
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		defer r.Close()
		b, e := io.ReadAll(io.LimitReader(contextReader{ctx, r}, max+1))
		if e != nil || int64(len(b)) > max {
			return nil, packError("archive entry read failed")
		}
		return b, nil
	}
	return nil, packError("archive index missing")
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func zipBounds(r io.ReaderAt, size int64) error {
	if size < 22 || size > MaxArchiveBytes {
		return packError("archive size exceeds bound")
	}
	n := min(size, int64(65557))
	tail := make([]byte, n)
	if _, e := r.ReadAt(tail, size-n); e != nil {
		return e
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
		ds := binary.LittleEndian.Uint32(end[12:16])
		off := binary.LittleEndian.Uint32(end[16:20])
		if binary.LittleEndian.Uint16(end[4:6]) != 0 || binary.LittleEndian.Uint16(end[6:8]) != 0 || binary.LittleEndian.Uint16(end[8:10]) != count || count > MaxPackFiles || ds > 8<<20 || uint64(off)+uint64(ds) > uint64(size-n+int64(i)) {
			return packError("ZIP directory exceeds bounded single-volume limits")
		}
		return nil
	}
	return packError("ZIP end record missing")
}
