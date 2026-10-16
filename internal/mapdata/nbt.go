package mapdata

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"unicode/utf16"
)

const maxDecoded = 8 << 20

var errCorrupt = errors.New("invalid or excessive saved data")
var errUnsupported = errors.New("unsupported saved data format")
var errStale = errors.New("saved data changed during read; retry")
var errMissing = errors.New("saved data is absent")

type tag struct {
	kind  byte
	value any
}
type compound map[string]tag
type list struct {
	kind   byte
	values []tag
}
type nbtReader struct {
	ctx        context.Context
	data       []byte
	pos, nodes int
}

func parseNBT(ctx context.Context, b []byte) (compound, error) {
	if len(b) > maxDecoded {
		return nil, errCorrupt
	}
	r := nbtReader{ctx: ctx, data: b}
	k, e := r.take(1)
	if e != nil || k[0] != 10 {
		return nil, errCorrupt
	}
	if _, e = r.str(); e != nil {
		return nil, e
	}
	n, e := r.payload(10, 0)
	if e != nil {
		return nil, e
	}
	if r.pos != len(b) {
		return nil, errCorrupt
	}
	return n.value.(compound), nil
}
func (r *nbtReader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.data)-r.pos {
		return nil, errCorrupt
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}
func (r *nbtReader) str() (string, error) {
	b, e := r.take(2)
	if e != nil {
		return "", e
	}
	b, e = r.take(int(binary.BigEndian.Uint16(b)))
	if e != nil {
		return "", e
	}
	return modifiedUTF(b)
}
func (r *nbtReader) count() (int, error) {
	b, e := r.take(4)
	if e != nil {
		return 0, e
	}
	n := int64(int32(binary.BigEndian.Uint32(b)))
	if n < 0 || n > maxDecoded {
		return 0, errCorrupt
	}
	return int(n), nil
}
func (r *nbtReader) payload(k byte, depth int) (tag, error) {
	if e := r.ctx.Err(); e != nil {
		return tag{}, e
	}
	r.nodes++
	if depth > 32 || r.nodes > 65536 {
		return tag{}, errCorrupt
	}
	n := tag{kind: k}
	switch k {
	case 1, 2, 3, 4, 5, 6:
		sizes := [7]int{0, 1, 2, 4, 8, 4, 8}
		b, e := r.take(sizes[k])
		if e != nil {
			return tag{}, e
		}
		switch k {
		case 1:
			n.value = int8(b[0])
		case 2:
			n.value = int16(binary.BigEndian.Uint16(b))
		case 3:
			n.value = int32(binary.BigEndian.Uint32(b))
		case 4:
			n.value = int64(binary.BigEndian.Uint64(b))
		case 5:
			n.value = math.Float32frombits(binary.BigEndian.Uint32(b))
		case 6:
			n.value = math.Float64frombits(binary.BigEndian.Uint64(b))
		}
	case 7, 11, 12:
		count, e := r.count()
		if e != nil {
			return tag{}, e
		}
		width := 1
		if k == 11 {
			width = 4
		}
		if k == 12 {
			width = 8
		}
		if count > maxDecoded/width {
			return tag{}, errCorrupt
		}
		b, e := r.take(count * width)
		if e != nil {
			return tag{}, e
		}
		if k == 7 {
			n.value = b
		} else if k == 11 {
			a := make([]int32, count)
			for i := range a {
				a[i] = int32(binary.BigEndian.Uint32(b[i*4:]))
			}
			n.value = a
		} else {
			a := make([]uint64, count)
			for i := range a {
				a[i] = binary.BigEndian.Uint64(b[i*8:])
			}
			n.value = a
		}
	case 8:
		s, e := r.str()
		if e != nil {
			return tag{}, e
		}
		n.value = s
	case 9:
		b, e := r.take(1)
		if e != nil {
			return tag{}, e
		}
		count, e := r.count()
		if e != nil {
			return tag{}, e
		}
		if count > 65536-r.nodes || b[0] > 12 || b[0] == 0 && count != 0 {
			return tag{}, errCorrupt
		}
		a := make([]tag, count)
		for i := range a {
			a[i], e = r.payload(b[0], depth+1)
			if e != nil {
				return tag{}, e
			}
		}
		n.value = list{b[0], a}
	case 10:
		a := compound{}
		for {
			b, e := r.take(1)
			if e != nil {
				return tag{}, e
			}
			if b[0] == 0 {
				break
			}
			s, e := r.str()
			if e != nil {
				return tag{}, e
			}
			if _, exists := a[s]; exists {
				return tag{}, errCorrupt
			}
			v, e := r.payload(b[0], depth+1)
			if e != nil {
				return tag{}, e
			}
			a[s] = v
		}
		n.value = a
	default:
		return tag{}, errCorrupt
	}
	return n, nil
}

// Java DataInput modified UTF-8 uses UTF-16 code units, encodes NUL as C0 80,
// and encodes supplementary characters as two three-byte surrogate units.
func modifiedUTF(b []byte) (string, error) {
	units := make([]uint16, 0, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		var u uint16
		switch {
		case c > 0 && c < 128:
			u = uint16(c)
			i++
		case c&0xe0 == 0xc0:
			if i+1 >= len(b) || b[i+1]&0xc0 != 0x80 {
				return "", errCorrupt
			}
			u = uint16(c&31)<<6 | uint16(b[i+1]&63)
			if u < 128 && !(c == 0xc0 && b[i+1] == 0x80) {
				return "", errCorrupt
			}
			i += 2
		case c&0xf0 == 0xe0:
			if i+2 >= len(b) || b[i+1]&0xc0 != 0x80 || b[i+2]&0xc0 != 0x80 {
				return "", errCorrupt
			}
			u = uint16(c&15)<<12 | uint16(b[i+1]&63)<<6 | uint16(b[i+2]&63)
			if u < 2048 {
				return "", errCorrupt
			}
			i += 3
		default:
			return "", errCorrupt
		}
		units = append(units, u)
	}
	for i := 0; i < len(units); i++ {
		u := units[i]
		if u >= 0xd800 && u <= 0xdbff {
			if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", errCorrupt
			}
			i++
		} else if u >= 0xdc00 && u <= 0xdfff {
			return "", errCorrupt
		}
	}
	return string(utf16.Decode(units)), nil
}
func field[T any](c compound, key string, kind byte) (T, error) {
	var zero T
	n, ok := c[key]
	if !ok || n.kind != kind {
		return zero, fmt.Errorf("field %s: %w", key, errCorrupt)
	}
	v, ok := n.value.(T)
	if !ok {
		return zero, errCorrupt
	}
	return v, nil
}
