package mapdata

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
)

type regionRead struct {
	region, external     *savedFile
	header, record, data []byte
	offset               int64
	codec                byte
	timestamp            uint32
}

func (r *regionRead) close() {
	if r.region != nil {
		r.region.f.Close()
	}
	if r.external != nil {
		r.external.f.Close()
	}
}
func (r *regionRead) verify(ctx context.Context) error {
	if e := r.region.verify(ctx, []span{{0, r.header}, {r.offset, r.record}}); e != nil {
		return e
	}
	if r.external != nil {
		return r.external.verify(ctx, []span{{0, r.data}})
	}
	return nil
}

// readRegion validates the complete sector table before reading a selected chunk.
// Negative coordinates use arithmetic shift and low five bits, matching floor division.
func readRegion(ctx context.Context, s *source, req Request) (out *regionRead, err error) {
	name := fmt.Sprintf("r.%d.%d.mca", req.ChunkX>>5, req.ChunkZ>>5)
	f, e := s.open(name, 1<<30)
	if os.IsNotExist(e) {
		return nil, errMissing
	}
	if e != nil {
		return nil, e
	}
	r := &regionRead{region: f}
	defer func() {
		if err != nil {
			if changed := r.verify(ctx); changed != nil {
				err = changed
			}
			r.close()
		}
	}()
	size := f.info.Size()
	if size < 8192 || size%4096 != 0 {
		return nil, errCorrupt
	}
	r.header, e = f.readAt(ctx, 0, 8192)
	if e != nil {
		return nil, e
	}
	type extent struct{ start, end uint32 }
	used := make([]extent, 0, 1024)
	for i := 0; i < 1024; i++ {
		loc := binary.BigEndian.Uint32(r.header[i*4:])
		if loc == 0 {
			continue
		}
		start, count := loc>>8, loc&255
		if start < 2 || count == 0 || int64(start+count) > size/4096 {
			return nil, errCorrupt
		}
		used = append(used, extent{start, start + count})
	}
	sort.Slice(used, func(i, j int) bool { return used[i].start < used[j].start })
	for i := 1; i < len(used); i++ {
		if used[i].start < used[i-1].end {
			return nil, errCorrupt
		}
	}
	index := int(req.ChunkX&31) + 32*int(req.ChunkZ&31)
	loc := binary.BigEndian.Uint32(r.header[index*4:])
	if loc == 0 {
		if e := f.verify(ctx, []span{{0, r.header}}); e != nil {
			return nil, e
		}
		return nil, errMissing
	}
	r.offset = int64(loc>>8) * 4096
	r.timestamp = binary.BigEndian.Uint32(r.header[4096+index*4:])
	head, e := f.readAt(ctx, r.offset, 5)
	if e != nil {
		return nil, e
	}
	length := int64(binary.BigEndian.Uint32(head))
	if length < 1 || length+4 > int64(loc&255)*4096 {
		return nil, errCorrupt
	}
	r.record, e = f.readAt(ctx, r.offset, int(length+4))
	if e != nil {
		return nil, e
	}
	r.codec = r.record[4] & 127
	if r.codec < 1 || r.codec > 4 {
		return nil, errUnsupported
	}
	if r.record[4]&128 != 0 {
		if length != 1 {
			return nil, errCorrupt
		}
		external, e := s.open(fmt.Sprintf("c.%d.%d.mcc", req.ChunkX, req.ChunkZ), 16<<20)
		if os.IsNotExist(e) {
			return nil, errStale
		}
		if e != nil {
			return nil, e
		}
		r.external = external
		r.data, e = external.readAt(ctx, 0, int(external.info.Size()))
		if e != nil {
			return nil, e
		}
	} else {
		r.data = r.record[5:]
	}
	return r, nil
}
