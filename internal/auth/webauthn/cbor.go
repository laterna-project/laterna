package webauthn

import (
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

// A CBOR reader (RFC 8949) cut down to what WebAuthn uses: integers, byte and text strings, arrays,
// maps, booleans and null, with definite lengths. Tags, floats and indefinite lengths are rejected.
// Depth and sizes are bounded.

var errCBOR = errors.New("invalid CBOR")

const (
	maxCBORDepth = 8
	maxCBORItems = 1024
)

// decodeCBOR reads one value at the start of b and returns the rest. Integers come back as int64
// and maps as map[any]any (integer or text keys).
func decodeCBOR(b []byte) (v any, rest []byte, err error) { return decodeItem(b, 0) }

func decodeItem(b []byte, depth int) (any, []byte, error) {
	if depth > maxCBORDepth || len(b) == 0 {
		return nil, nil, errCBOR
	}
	major, info := b[0]>>5, b[0]&0x1f
	b = b[1:]
	if major == 7 {
		switch info {
		case 20:
			return false, b, nil
		case 21:
			return true, b, nil
		case 22:
			return nil, b, nil
		}
		return nil, nil, errCBOR
	}
	n, b, err := argument(info, b)
	if err != nil {
		return nil, nil, err
	}
	switch major {
	case 0:
		if n > math.MaxInt64 {
			return nil, nil, errCBOR
		}
		return int64(n), b, nil
	case 1:
		if n > math.MaxInt64 {
			return nil, nil, errCBOR
		}
		return -1 - int64(n), b, nil
	case 2, 3:
		if n > uint64(len(b)) {
			return nil, nil, errCBOR
		}
		data := b[:n]
		if major == 3 {
			if !utf8.Valid(data) {
				return nil, nil, errCBOR
			}
			return string(data), b[n:], nil
		}
		return append([]byte(nil), data...), b[n:], nil
	case 4:
		if n > maxCBORItems {
			return nil, nil, errCBOR
		}
		out := make([]any, 0, n)
		for range n {
			var v any
			if v, b, err = decodeItem(b, depth+1); err != nil {
				return nil, nil, err
			}
			out = append(out, v)
		}
		return out, b, nil
	case 5:
		if n > maxCBORItems {
			return nil, nil, errCBOR
		}
		out := make(map[any]any, n)
		for range n {
			var k, v any
			if k, b, err = decodeItem(b, depth+1); err != nil {
				return nil, nil, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, nil, errCBOR
			}
			if _, dup := out[k]; dup {
				return nil, nil, errCBOR
			}
			if v, b, err = decodeItem(b, depth+1); err != nil {
				return nil, nil, err
			}
			out[k] = v
		}
		return out, b, nil
	}
	return nil, nil, errCBOR // tags (6)
}

// argument reads the argument of a CBOR head (value, length or item count).
func argument(info byte, b []byte) (uint64, []byte, error) {
	switch {
	case info < 24:
		return uint64(info), b, nil
	case info == 24 && len(b) >= 1:
		return uint64(b[0]), b[1:], nil
	case info == 25 && len(b) >= 2:
		return uint64(binary.BigEndian.Uint16(b)), b[2:], nil
	case info == 26 && len(b) >= 4:
		return uint64(binary.BigEndian.Uint32(b)), b[4:], nil
	case info == 27 && len(b) >= 8:
		return binary.BigEndian.Uint64(b), b[8:], nil
	}
	return 0, nil, errCBOR
}
