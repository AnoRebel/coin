// Package protocol implements the ARNE wire format: deterministic CBOR,
// canonical asset/account/transaction types, addresses, and signing.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Canonical CBOR (RFC 8949 §4.2.1) with a restricted type set:
// nil, bool, uint64/int64, []byte, string, []any, map[string]any.
// Floats, tags, indefinite lengths, and non-string map keys are rejected so
// that Go and Python always produce identical bytes.

const maxDepth = 64

var (
	ErrCBOR       = errors.New("cbor: malformed input")
	ErrNonCanon   = errors.New("cbor: non-canonical input")
	ErrCBORType   = errors.New("cbor: unsupported type")
	ErrCBORLimits = errors.New("cbor: depth limit exceeded")
)

// Encode returns the canonical CBOR encoding of v.
func Encode(v any) ([]byte, error) { return appendCBOR(nil, v, 0) }

func appendCBOR(dst []byte, v any, depth int) ([]byte, error) {
	if depth > maxDepth {
		return nil, ErrCBORLimits
	}
	switch x := v.(type) {
	case nil:
		return append(dst, 0xf6), nil
	case bool:
		if x {
			return append(dst, 0xf5), nil
		}
		return append(dst, 0xf4), nil
	case uint64:
		return appendHead(dst, 0, x), nil
	case uint:
		return appendHead(dst, 0, uint64(x)), nil
	case uint32:
		return appendHead(dst, 0, uint64(x)), nil
	case uint16:
		return appendHead(dst, 0, uint64(x)), nil
	case uint8:
		return appendHead(dst, 0, uint64(x)), nil
	case int64:
		if x >= 0 {
			return appendHead(dst, 0, uint64(x)), nil
		}
		return appendHead(dst, 1, uint64(-1-x)), nil
	case int:
		return appendCBOR(dst, int64(x), depth)
	case int32:
		return appendCBOR(dst, int64(x), depth)
	case string:
		dst = appendHead(dst, 3, uint64(len(x)))
		return append(dst, x...), nil
	case []byte:
		dst = appendHead(dst, 2, uint64(len(x)))
		return append(dst, x...), nil
	case []any:
		dst = appendHead(dst, 4, uint64(len(x)))
		for _, item := range x {
			var err error
			if dst, err = appendCBOR(dst, item, depth+1); err != nil {
				return nil, err
			}
		}
		return dst, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			ei, _ := appendCBOR(nil, keys[i], 0)
			ej, _ := appendCBOR(nil, keys[j], 0)
			if len(ei) != len(ej) {
				return len(ei) < len(ej)
			}
			return string(ei) < string(ej)
		})
		dst = appendHead(dst, 5, uint64(len(keys)))
		for _, k := range keys {
			var err error
			if dst, err = appendCBOR(dst, k, depth+1); err != nil {
				return nil, err
			}
			if dst, err = appendCBOR(dst, x[k], depth+1); err != nil {
				return nil, err
			}
		}
		return dst, nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrCBORType, v)
	}
}

func appendHead(dst []byte, major byte, n uint64) []byte {
	m := major << 5
	switch {
	case n < 24:
		return append(dst, m|byte(n))
	case n <= math.MaxUint8:
		return append(dst, m|24, byte(n))
	case n <= math.MaxUint16:
		dst = append(dst, m|25)
		return binary.BigEndian.AppendUint16(dst, uint16(n))
	default:
		if n <= math.MaxUint32 {
			dst = append(dst, m|26)
			return binary.BigEndian.AppendUint32(dst, uint32(n))
		}
		dst = append(dst, m|27)
		return binary.BigEndian.AppendUint64(dst, n)
	}
}

// Decode parses canonical CBOR and returns nil, bool, uint64, int64, []byte,
// string, []any, or map[string]any. int64 is used only for negative ints.
func Decode(b []byte) (any, error) {
	v, off, err := decodeItem(b, 0, 0)
	if err != nil {
		return nil, err
	}
	if off != len(b) {
		return nil, ErrCBOR
	}
	return v, nil
}

// DecodeMap decodes b and requires a map.
func DecodeMap(b []byte) (map[string]any, error) {
	v, err := Decode(b)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrCBOR
	}
	return m, nil
}

func decodeItem(b []byte, off, depth int) (any, int, error) {
	if depth > maxDepth {
		return nil, 0, ErrCBORLimits
	}
	if off >= len(b) {
		return nil, 0, ErrCBOR
	}
	ib := b[off]
	major, info := ib>>5, ib&0x1f
	val, next, err := decodeHead(b, off, info)
	if err != nil {
		return nil, 0, err
	}
	switch major {
	case 7:
		switch info {
		case 20:
			return false, next, nil
		case 21:
			return true, next, nil
		case 22:
			return nil, next, nil
		default:
			return nil, 0, ErrCBOR
		}
	case 0:
		return val, next, nil
	case 1:
		if val > math.MaxInt64 {
			return nil, 0, ErrCBOR
		}
		return -1 - int64(val), next, nil
	case 2, 3:
		if val > uint64(len(b)-next) {
			return nil, 0, ErrCBOR
		}
		end := next + int(val)
		if major == 2 {
			return append([]byte(nil), b[next:end]...), end, nil
		}
		return string(b[next:end]), end, nil
	case 4:
		if val > uint64(len(b)-next) {
			return nil, 0, ErrCBOR
		}
		arr := make([]any, 0, val)
		for i := uint64(0); i < val; i++ {
			var err error
			var item any
			if item, next, err = decodeItem(b, next, depth+1); err != nil {
				return nil, 0, err
			}
			arr = append(arr, item)
		}
		return arr, next, nil
	case 5:
		if val > uint64(len(b)-next) {
			return nil, 0, ErrCBOR
		}
		m := make(map[string]any, val)
		var prevKey []byte
		for i := uint64(0); i < val; i++ {
			start := next
			var k any
			if k, next, err = decodeItem(b, next, depth+1); err != nil {
				return nil, 0, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, 0, fmt.Errorf("%w: map key must be text", ErrCBORType)
			}
			enc := b[start:next]
			if prevKey != nil && !canonicalKeyLess(prevKey, enc) {
				return nil, 0, ErrNonCanon
			}
			prevKey = enc
			if _, dup := m[ks]; dup {
				return nil, 0, ErrNonCanon
			}
			var item any
			if item, next, err = decodeItem(b, next, depth+1); err != nil {
				return nil, 0, err
			}
			m[ks] = item
		}
		return m, next, nil
	default:
		return nil, 0, ErrCBOR
	}
}

func canonicalKeyLess(a, b []byte) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return string(a) < string(b)
}

func decodeHead(b []byte, off int, info byte) (uint64, int, error) {
	if info < 24 {
		return uint64(info), off + 1, nil
	}
	var n int
	switch info {
	case 24:
		n = 1
	case 25:
		n = 2
	case 26:
		n = 4
	case 27:
		n = 8
	default:
		return 0, 0, ErrCBOR // indefinite lengths and reserved values
	}
	if off+1+n > len(b) {
		return 0, 0, ErrCBOR
	}
	var v uint64
	for _, c := range b[off+1 : off+1+n] {
		v = v<<8 | uint64(c)
	}
	// Require the shortest encoding.
	if (n == 1 && v < 24) || (n == 2 && v <= math.MaxUint8) ||
		(n == 4 && v <= math.MaxUint16) || (n == 8 && v <= math.MaxUint32) {
		return 0, 0, ErrNonCanon
	}
	return v, off + 1 + n, nil
}
