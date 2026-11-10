package winrt

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// guid is the 16-byte binary layout RoGetActivationFactory and
// QueryInterface take: the three integer fields little-endian, then the last
// eight bytes verbatim. Spelled out rather than borrowed so this package needs
// nothing beyond the standard library.
type guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

// guidGroupWidths are the digit counts of a GUID's five hyphen-separated groups,
// in order. They are fixed by the canonical text form.
var guidGroupWidths = [5]int{8, 4, 4, 4, 12}

// parseGUID reads the braced or bare canonical form of a GUID, the way the
// Windows SDK headers print interface IDs: "{5BFFA85A-3384-3540-9940-699120D428A8}".
func parseGUID(value string) (guid, error) {
	text := strings.TrimSpace(value)

	switch {
	case strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}"):
		text = text[1 : len(text)-1]
	case strings.HasPrefix(text, "{") || strings.HasSuffix(text, "}"):
		return guid{}, fmt.Errorf("invalid GUID %q: unbalanced braces", value)
	}

	groups := strings.Split(text, "-")
	if len(groups) != len(guidGroupWidths) {
		return guid{}, fmt.Errorf("invalid GUID %q: want five hyphen-separated groups", value)
	}

	var raw [16]byte

	offset := 0

	for index, group := range groups {
		width := guidGroupWidths[index]
		if len(group) != width {
			return guid{}, fmt.Errorf(
				"invalid GUID %q: group %d has %d digits, want %d",
				value,
				index+1,
				len(group),
				width,
			)
		}

		if _, err := hex.Decode(raw[offset:offset+width/2], []byte(group)); err != nil {
			return guid{}, fmt.Errorf("invalid GUID %q: %w", value, err)
		}

		offset += width / 2
	}

	return guid{
		data1: binary.BigEndian.Uint32(raw[0:4]),
		data2: binary.BigEndian.Uint16(raw[4:6]),
		data3: binary.BigEndian.Uint16(raw[6:8]),
		data4: [8]byte(raw[8:16]),
	}, nil
}

// mustGUID parses an interface ID written in the source. A typo is a programming
// error, and a wrong ID calls into the wrong interface, so it panics while the
// package is loading rather than misbehaving at first use.
func mustGUID(value string) guid {
	parsed, err := parseGUID(value)
	if err != nil {
		panic(err)
	}

	return parsed
}
