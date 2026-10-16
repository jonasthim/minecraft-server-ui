package mapdata

import (
	"context"
	"fmt"
	"math/bits"
	"regexp"
	"sort"
)

var blockName = regexp.MustCompile(`^[a-z0-9_.-]+:[a-z0-9_./-]+$`)

// Colors are original representative map colors; no game artwork is used.
var colors = map[string]string{
	"minecraft:air": "#000000", "minecraft:cave_air": "#000000", "minecraft:void_air": "#000000",
	"minecraft:stone": "#888b91", "minecraft:deepslate": "#4e5159", "minecraft:grass_block": "#6b9850",
	"minecraft:dirt": "#927357", "minecraft:water": "#386cad", "minecraft:lava": "#df742b",
	"minecraft:sand": "#d5c48e", "minecraft:gravel": "#aaa29a", "minecraft:bedrock": "#474954",
	"minecraft:snow": "#e1ebef", "minecraft:snow_block": "#e1ebef", "minecraft:ice": "#8eb6d2",
	"minecraft:netherrack": "#975c5c", "minecraft:end_stone": "#c5c08c", "minecraft:oak_log": "#7b6649",
	"minecraft:oak_leaves": "#4b7b42", "minecraft:glass": "#b4d2d9", "minecraft:coal_ore": "#62666b",
}
var legacyNames = map[int]string{0: "air", 1: "stone", 2: "grass_block", 3: "dirt", 7: "bedrock", 8: "water", 9: "water", 10: "lava", 11: "lava", 12: "sand", 13: "gravel", 16: "coal_ore", 17: "oak_log", 18: "oak_leaves", 20: "glass", 78: "snow", 79: "ice", 80: "snow_block", 87: "netherrack", 121: "end_stone"}

func cell(block string, y int32) Cell {
	color, known := colors[block]
	if !known {
		color = "#b064bd"
	}
	return Cell{Y: y, Block: block, Color: color, Known: known, Empty: block == "minecraft:air" || block == "minecraft:cave_air" || block == "minecraft:void_air"}
}

type section struct {
	y      int32
	blocks [4096]string
}

func decodeChunk(ctx context.Context, b []byte, req Request) (Tile, error) {
	out := Tile{Dimension: req.Dimension, ChunkX: req.ChunkX, ChunkZ: req.ChunkZ, MaxY: req.MaxY, Consistency: Consistency}
	root, e := parseNBT(ctx, b)
	if e != nil {
		return out, e
	}
	dv, e := field[int32](root, "DataVersion", 3)
	if e != nil {
		return out, e
	}
	out.DataVersion = dv
	if !supportedDataVersion(dv) {
		return out, errUnsupported
	}
	modern := dv >= 2860
	c := root
	if !modern {
		c, e = field[compound](root, "Level", 10)
		if e != nil {
			return out, e
		}
	}
	x, e := field[int32](c, "xPos", 3)
	if e != nil {
		return out, e
	}
	z, e := field[int32](c, "zPos", 3)
	if e != nil || x != req.ChunkX || z != req.ChunkZ {
		return out, errCorrupt
	}
	key := "Sections"
	if modern {
		key = "sections"
		status, e := field[string](c, "Status", 8)
		if e != nil {
			return out, e
		}
		if status != "full" && status != "minecraft:full" {
			return out, fmt.Errorf("incomplete generation: %w", errUnsupported)
		}
	}
	ss, e := field[list](c, key, 9)
	if e != nil {
		return out, e
	}
	if len(ss.values) > 64 || len(ss.values) > 0 && ss.kind != 10 {
		return out, errCorrupt
	}
	if dv < 1451 {
		out.Format = "legacy"
	} else if dv < 2529 {
		out.Format = "palette_dense"
	} else if modern {
		out.Format = "modern"
	} else {
		out.Format = "palette_padded"
	}
	sections := make([]section, 0, len(ss.values))
	seen := map[int32]bool{}
	for _, s := range ss.values {
		if e := ctx.Err(); e != nil {
			return out, e
		}
		sc := s.value.(compound)
		sy, e := field[int8](sc, "Y", 1)
		if e != nil {
			return out, e
		}
		y := int32(sy) * 16
		if y < -2032 || y+15 > 2031 || seen[y] {
			return out, errCorrupt
		}
		seen[y] = true
		if !modern && (y < 0 || y > 255) {
			return out, errCorrupt
		}
		sec := section{y: y}
		if dv < 1451 {
			e = decodeLegacy(sc, &sec)
		} else {
			e = decodePaletted(sc, &sec, modern, dv >= 2529)
		}
		if e != nil {
			return out, e
		}
		sections = append(sections, sec)
	}
	sort.Slice(sections, func(i, j int) bool { return sections[i].y > sections[j].y })
	out.Cells = make([]Cell, 256)
	for i := range out.Cells {
		out.Cells[i] = cell("minecraft:air", 0)
	}
	maxY := int32(2031)
	if req.MaxY != nil {
		maxY = *req.MaxY
	}
	for _, s := range sections {
		for y := 15; y >= 0; y-- {
			if e := ctx.Err(); e != nil {
				return out, e
			}
			worldY := s.y + int32(y)
			if worldY > maxY {
				continue
			}
			for i := 0; i < 256; i++ {
				if !out.Cells[i].Empty {
					continue
				}
				c := cell(s.blocks[y*256+i], worldY)
				if !c.Empty {
					out.Cells[i] = c
				}
			}
		}
	}
	out.State = Ready
	return out, nil
}
func decodeLegacy(c compound, s *section) error {
	b, e := field[[]byte](c, "Blocks", 7)
	if e != nil || len(b) != 4096 {
		return errCorrupt
	}
	d, e := field[[]byte](c, "Data", 7)
	if e != nil || len(d) != 2048 {
		return errCorrupt
	}
	var add []byte
	if n, ok := c["Add"]; ok {
		if n.kind != 7 {
			return errCorrupt
		}
		add = n.value.([]byte)
		if len(add) != 2048 {
			return errCorrupt
		}
	}
	for i := range s.blocks {
		id := int(b[i])
		if add != nil {
			id |= int((add[i/2]>>uint(4*(i%2)))&15) << 8
		}
		meta := (d[i/2] >> uint(4*(i%2))) & 15
		if name, ok := legacyNames[id]; ok && (meta == 0 || id == 0 || id >= 8 && id <= 11) {
			s.blocks[i] = "minecraft:" + name
		} else {
			s.blocks[i] = fmt.Sprintf("legacy:%d:%d", id, meta)
		}
	}
	return nil
}
func decodePaletted(c compound, s *section, modern, padded bool) error {
	pk, dk := "Palette", "BlockStates"
	if modern {
		var e error
		if _, has := c["block_states"]; !has {
			validLight := false
			for _, name := range []string{"SkyLight", "BlockLight"} {
				if light, ok := c[name]; ok {
					if light.kind != 7 || len(light.value.([]byte)) != 2048 {
						return errCorrupt
					}
					validLight = true
				}
			}
			if !validLight {
				return errUnsupported
			}
			for i := range s.blocks {
				s.blocks[i] = "minecraft:air"
			}
			return nil
		}
		c, e = field[compound](c, "block_states", 10)
		if e != nil {
			return e
		}
		pk, dk = "palette", "data"
	}
	p, e := field[list](c, pk, 9)
	if e != nil || p.kind != 10 || len(p.values) == 0 || len(p.values) > 4096 {
		return errCorrupt
	}
	palette := make([]string, len(p.values))
	for i, v := range p.values {
		name, e := field[string](v.value.(compound), "Name", 8)
		if e != nil || len(name) > 255 || !blockName.MatchString(name) {
			return errCorrupt
		}
		palette[i] = name
	}
	width := max(4, bits.Len(uint(len(palette)-1)))
	// Disk NBT has no declared width; it is determined by palette cardinality.
	// Refuse explicit nonstandard width tags rather than guessing a mod's encoding.
	for _, k := range []string{"bits", "bits_per_entry"} {
		if _, ok := c[k]; ok {
			return errUnsupported
		}
	}
	raw, has := c[dk]
	if !has {
		if len(palette) != 1 || !modern {
			return errCorrupt
		}
		for i := range s.blocks {
			s.blocks[i] = palette[0]
		}
		return nil
	}
	if raw.kind != 12 {
		return errCorrupt
	}
	data := raw.value.([]uint64)
	want := (4096*width + 63) / 64
	if padded {
		want = (4096 + (64 / width) - 1) / (64 / width)
	}
	if len(data) != want {
		return errCorrupt
	}
	mask := uint64(1<<width) - 1
	for i := range s.blocks {
		var n uint64
		if padded {
			per := 64 / width
			n = data[i/per] >> uint((i%per)*width) & mask
		} else {
			offset := i * width
			idx, shift := offset/64, offset%64
			n = data[idx] >> uint(shift)
			if shift+width > 64 {
				n |= data[idx+1] << uint(64-shift)
			}
			n &= mask
		}
		if n >= uint64(len(palette)) {
			return errCorrupt
		}
		s.blocks[i] = palette[n]
	}
	return nil
}

// Known release ranges are format routing evidence, not release qualification.
// Snapshot transition gaps are deliberately refused instead of guessed.
func supportedDataVersion(v int32) bool {
	return v == 1343 || v >= 1519 && v <= 2230 || v >= 2566 && v <= 2730 || v >= 2860 && v <= maxDataVersion
}

// Mojang official26.3 client version.json, SHA1e877b6a07acd633fb3bb475002175cec036e7b87.
const maxDataVersion int32 = 5023
