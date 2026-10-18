// Package version holds the single product version shared by every binary.
package version

import "strings"

// Version is the product version. mygo.json must carry the same value
// (checked by TestManifestMatches); release tags are "v" + Version. The Rust
// core has no version of its own: its hosts pass this one when they start it.
const Version = "0.1.2"

// Older reports whether version a ("0.1.0", "v0.1.2") is older than b.
func Older(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	var out [3]int
	i := 0
	for _, r := range strings.TrimPrefix(v, "v") {
		switch {
		case r == '.':
			i++
			if i > 2 {
				return out
			}
		case r >= '0' && r <= '9':
			out[i] = out[i]*10 + int(r-'0')
		default:
			return out
		}
	}
	return out
}
