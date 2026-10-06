package deps

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
)

// regMultiSZ encodes strings as a Windows REG_MULTI_SZ: each string is
// UTF-16LE and NUL-terminated, and the whole list ends with one extra NUL.
// It rejects embedded NULs (which Windows' own UTF16FromString panics on).
func regMultiSZ(strs ...string) ([]byte, error) {
	var units []uint16
	for _, s := range strs {
		if strings.IndexByte(s, 0) >= 0 {
			return nil, errors.New("deps: NUL in REG_MULTI_SZ string")
		}
		units = append(units, utf16.Encode([]rune(s))...)
		units = append(units, 0) // terminate this string
	}
	units = append(units, 0) // terminate the list
	out := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[i*2:], u)
	}
	return out, nil
}
