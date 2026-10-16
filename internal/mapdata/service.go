package mapdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
	"hash"
	"os"
	"strings"
	"time"
)

type Service struct {
	discoverer Discoverer
	paths      instance.Paths
	admission  chan struct{}
}

func New(discoverer Discoverer, paths instance.Paths) *Service {
	return &Service{discoverer: discoverer, paths: paths, admission: make(chan struct{}, 2)}
}
func (s *Service) enter(ctx context.Context) (context.Context, func(), error) {
	if e := ctx.Err(); e != nil {
		return ctx, nil, e
	}
	select {
	case s.admission <- struct{}{}:
	default:
		return ctx, nil, domain.E(domain.CodeUnavailable, "map reader is busy; retry")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	return ctx, func() { cancel(); <-s.admission }, nil
}
func validateRequest(r Request) error {
	if r.Dimension == "" || len(r.Dimension) > 1024 || !(blockName.MatchString(r.Dimension) || strings.HasPrefix(r.Dimension, "unknown:") && len(r.Dimension) < 256 && !strings.ContainsAny(r.Dimension, "/\\\n\r\x00")) || strings.Contains(r.Dimension, "..") || r.ChunkX < -1875000 || r.ChunkX > 1874999 || r.ChunkZ < -1875000 || r.ChunkZ > 1874999 || r.MaxY != nil && (*r.MaxY < -2032 || *r.MaxY > 2031) {
		return domain.E(domain.CodeValidationFailed, "invalid dimension, chunk coordinate, or height")
	}
	return nil
}
func discoveryState(d world.Discovery) (State, string) {
	switch d.State {
	case "present":
		return Ready, ""
	case "absent":
		return Missing, "Saved world is absent."
	case "unsupported":
		return Unsupported, "Configured world layout is unsupported."
	default:
		return Unknown, "Saved world layout or identity is ambiguous."
	}
}
func status(err error) (State, string, error) {
	switch {
	case err == nil:
		return Ready, "", nil
	case errors.Is(err, errMissing), os.IsNotExist(err):
		return Missing, "Saved world, dimension, or chunk is absent.", nil
	case errors.Is(err, errStale):
		return Stale, "Saved source changed during read; retry.", nil
	case errors.Is(err, errUnsupported):
		return Unsupported, "Saved codec, DataVersion, generation stage, or schema is unsupported.", nil
	case errors.Is(err, errCorrupt):
		return Corrupt, "Saved data is malformed or exceeds decoder bounds.", nil
	default:
		return Unknown, "", fmt.Errorf("read saved map data: %w", err)
	}
}
func (s *Service) discover(ctx context.Context, id string) (world.Discovery, error) {
	if e := domain.ValidateInstanceID(id); e != nil {
		return world.Discovery{}, domain.Wrap(domain.CodeValidationFailed, "invalid instance id", e)
	}
	if s.discoverer == nil {
		return world.Discovery{}, domain.E(domain.CodeUnavailable, "world discovery is not configured")
	}
	return s.discoverer.Discover(ctx, id)
}
func (s *Service) openRelative(id, relative string) (*source, error) {
	if _, e := s.paths.Resolve(id, "server", relative); e != nil {
		return nil, e
	}
	dirs, e := s.paths.For(id)
	if e != nil {
		return nil, e
	}
	src, e := openSource(dirs.Server)
	if e != nil {
		return nil, e
	}
	if e = src.descend(relative); e != nil {
		src.Close()
		return nil, e
	}
	return src, nil
}

type metadataRead struct {
	source  *source
	file    *savedFile
	raw     []byte
	version int32
	name    string
}

func (m *metadataRead) close() { m.file.f.Close(); m.source.Close() }
func (m *metadataRead) verify(ctx context.Context) error {
	if e := m.file.verify(ctx, []span{{0, m.raw}}); e != nil {
		return e
	}
	return m.source.verify()
}
func (s *Service) metadata(ctx context.Context, id string, d world.Discovery) (out *metadataRead, err error) {
	src, e := s.openRelative(id, d.LevelName)
	if e != nil {
		return nil, e
	}
	f, e := src.open("level.dat", 16<<20)
	if e != nil {
		src.Close()
		return nil, e
	}
	m := &metadataRead{source: src, file: f}
	defer func() {
		if err != nil {
			if changed := m.verify(ctx); changed != nil {
				err = changed
			}
			m.close()
		}
	}()
	m.raw, e = f.readAt(ctx, 0, int(f.info.Size()))
	if e != nil {
		return nil, e
	}
	decoded, e := decompress(ctx, 1, m.raw)
	if e != nil {
		return nil, e
	}
	root, e := parseNBT(ctx, decoded)
	if e != nil {
		return nil, e
	}
	data, e := field[compound](root, "Data", 10)
	if e != nil {
		return nil, e
	}
	m.version, e = field[int32](data, "DataVersion", 3)
	if e != nil {
		return nil, e
	}
	if !supportedDataVersion(m.version) {
		return nil, errUnsupported
	}
	if v, ok := data["Version"]; ok {
		if v.kind != 10 {
			return nil, errCorrupt
		}
		vc := v.value.(compound)
		m.name, e = field[string](vc, "Name", 8)
		if e != nil || len(m.name) > 128 {
			return nil, errCorrupt
		}
		if v, ok := vc["Id"]; ok {
			if v.kind != 3 || v.value.(int32) != m.version {
				return nil, errCorrupt
			}
		}
	}
	return m, nil
}
func (s *Service) Summary(ctx context.Context, id string) (Summary, error) {
	out := Summary{SampledAt: time.Now().UTC(), Consistency: Consistency}
	ctx, leave, e := s.enter(ctx)
	if e != nil {
		return out, e
	}
	defer leave()
	d, e := s.discover(ctx, id)
	if e != nil {
		return out, e
	}
	out.Discovery = d
	out.State, out.Reason = discoveryState(d)
	if out.State != Ready {
		return out, nil
	}
	m, e := s.metadata(ctx, id, d)
	if e != nil {
		out.State, out.Reason, e = status(e)
		return out, e
	}
	defer m.close()
	out.DataVersion = m.version
	out.VersionName = m.name
	out.SourceMTime = m.file.info.ModTime().UTC()
	e = m.verify(ctx)
	out.State, out.Reason, e = status(e)
	return out, e
}
func (s *Service) Tile(ctx context.Context, id string, req Request) (out Tile, err error) {
	out = Tile{Dimension: req.Dimension, ChunkX: req.ChunkX, ChunkZ: req.ChunkZ, MaxY: req.MaxY, SampledAt: time.Now().UTC(), Consistency: Consistency}
	if e := validateRequest(req); e != nil {
		return out, e
	}
	ctx, leave, e := s.enter(ctx)
	if e != nil {
		return out, e
	}
	defer leave()
	d, e := s.discover(ctx, id)
	if e != nil {
		return out, e
	}
	out.State, out.Reason = discoveryState(d)
	if out.State != Ready {
		return out, nil
	}
	var dim *world.Dimension
	for i := range d.Dimensions {
		if d.Dimensions[i].ID == req.Dimension {
			dim = &d.Dimensions[i]
			break
		}
	}
	if dim == nil {
		return out, domain.E(domain.CodeValidationFailed, "dimension was not found in configured world discovery")
	}
	if dim.Kind == "unknown" {
		out.State = Unknown
		out.Reason = "Mod dimension identity and format are unknown."
		return out, nil
	}
	if dim.State != "present" {
		out.State = Missing
		out.Reason = "Saved dimension is absent."
		return out, nil
	}
	// Every selected source is pinned until decoding and final verification finish.
	// Errors after any read are verified first, so observed live writes report stale.
	var m *metadataRead
	var src *source
	var r *regionRead
	defer func() {
		var changed error
		if r != nil {
			changed = r.verify(ctx)
		}
		if src != nil {
			if e := src.verify(); e != nil {
				changed = e
			}
		}
		if m != nil {
			if e := m.verify(ctx); e != nil {
				changed = e
			}
		}
		if changed != nil {
			err = changed
		}
		if err != nil {
			out.Cells = nil
			out.Fingerprint = ""
			out.State, out.Reason, err = status(err)
		}
		if r != nil {
			r.close()
		}
		if src != nil {
			src.Close()
		}
		if m != nil {
			m.close()
		}
	}()
	m, err = s.metadata(ctx, id, d)
	if err != nil {
		return out, err
	}
	src, err = s.openRelative(id, dim.Path+"/region")
	if err != nil {
		return out, err
	}
	r, err = readRegion(ctx, src, req)
	if err != nil {
		return out, err
	}
	decoded, e := decompress(ctx, r.codec, r.data)
	if e != nil {
		return out, e
	}
	tile, e := decodeChunk(ctx, decoded, req)
	if e != nil {
		return out, e
	}
	tile.SampledAt = out.SampledAt
	out = tile
	if r.timestamp != 0 {
		saved := time.Unix(int64(r.timestamp), 0).UTC()
		out.ChunkSavedAt = &saved
	}
	out.Codec = map[byte]string{1: "gzip", 2: "zlib", 3: "raw", 4: "lz4_java_block"}[r.codec]
	out.SourceMTime = r.region.info.ModTime().UTC()
	if r.external != nil && r.external.info.ModTime().After(out.SourceMTime) {
		out.SourceMTime = r.external.info.ModTime().UTC()
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00", policy, id, dim.ID, dim.Path, req.ChunkX, req.ChunkZ)
	if req.MaxY != nil {
		fmt.Fprintf(h, "%d", *req.MaxY)
	} else {
		fmt.Fprint(h, "surface")
	}
	hashSource(h, m.file, m.raw)
	hashSource(h, r.region, r.header)
	h.Write(r.record)
	if r.external != nil {
		hashSource(h, r.external, r.data)
	}
	out.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
func hashSource(h hash.Hash, f *savedFile, b []byte) {
	fmt.Fprintf(h, "\x00%s\x00%d\x00%d\x00", f.identity, f.info.Size(), f.info.ModTime().UnixNano())
	h.Write(b)
}
