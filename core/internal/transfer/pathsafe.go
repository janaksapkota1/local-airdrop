package transfer

import (
	"errors"
	"strings"
)

var errBadPath = errors.New("unsafe or invalid path")

// SafeSegments validates a relative "/"-separated path from a remote peer and
// returns its segments. It rejects anything that could escape the download
// directory or produce odd filenames.
func SafeSegments(p string) ([]string, error) {
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return nil, errBadPath
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || len(s) > 255 || strings.HasSuffix(s, partSuffix) {
			return nil, errBadPath
		}
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				return nil, errBadPath
			}
		}
	}
	return segs, nil
}
