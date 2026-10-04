package fingerprint

import (
	"encoding/binary"
	"errors"
	"time"
)

// magic starts a stored fingerprint. The digit changes whenever Compute does, so a fingerprint from
// an older algorithm cannot be read anymore and gets recomputed.
const magic = "LFP1"

// ErrFormat is returned for a stored fingerprint that is unreadable or comes from an older
// algorithm.
var ErrFormat = errors.New("unreadable audio fingerprint")

// MarshalBinary stores the fingerprint: header, start and length in milliseconds, frame count,
// codes, then one bit per silent frame.
func (p Print) MarshalBinary() ([]byte, error) {
	n := len(p.Codes)
	out := make([]byte, 0, len(magic)+20+4*n+(n+7)/8)
	out = append(out, magic...)
	out = binary.LittleEndian.AppendUint64(out, uint64(p.Start.Milliseconds()))  //nolint:gosec // positive duration
	out = binary.LittleEndian.AppendUint64(out, uint64(p.Length.Milliseconds())) //nolint:gosec // positive duration
	out = binary.LittleEndian.AppendUint32(out, uint32(n))                       //nolint:gosec // a few thousand frames
	for _, c := range p.Codes {
		out = binary.LittleEndian.AppendUint32(out, c)
	}
	silent := make([]byte, (n+7)/8)
	for i, s := range p.Silent {
		if s {
			silent[i/8] |= 1 << (i % 8)
		}
	}
	return append(out, silent...), nil
}

// UnmarshalBinary reads back a fingerprint stored by MarshalBinary.
func (p *Print) UnmarshalBinary(data []byte) error {
	const head = len(magic) + 20
	if len(data) < head || string(data[:len(magic)]) != magic {
		return ErrFormat
	}
	start := binary.LittleEndian.Uint64(data[len(magic):])
	length := binary.LittleEndian.Uint64(data[len(magic)+8:])
	n := int(binary.LittleEndian.Uint32(data[len(magic)+16:]))
	if len(data) != head+4*n+(n+7)/8 {
		return ErrFormat
	}
	codes := make([]uint32, n)
	for i := range codes {
		codes[i] = binary.LittleEndian.Uint32(data[head+4*i:])
	}
	bitmap := data[head+4*n:]
	silent := make([]bool, n)
	for i := range silent {
		silent[i] = bitmap[i/8]&(1<<(i%8)) != 0
	}
	*p = Print{
		Start:  time.Duration(start) * time.Millisecond,  //nolint:gosec // written by MarshalBinary
		Length: time.Duration(length) * time.Millisecond, //nolint:gosec // written by MarshalBinary
		Codes:  codes, Silent: silent,
	}
	return nil
}
