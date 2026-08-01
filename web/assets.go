package web

import (
	"net/url"
	"slices"
	"strings"
)

// The third-party code the page loads, each pinned to an exact version and to
// the hash of the exact bytes that version publishes.
//
// The hash is the security control, not the origin: the content security policy
// has to name the CDN as an allowed origin, so without the hash anything that
// origin served would run — on a page whose whole purpose is handing out a
// shell. The browser checks what it receives against the hash and refuses to
// execute or apply a mismatch, which is what makes serving this from someone
// else's infrastructure acceptable at all.
//
// The hashes below were taken from these exact URLs and confirmed to be npm's
// published artifacts rather than one CDN's, by checking that jsdelivr and
// unpkg serve them byte for byte identically.
//
// Two things must stay true, and neither fails visibly:
//
//   - The version is exact, never a range. A range serves different bytes on
//     the next publish, and the integrity check then takes the page down.
//   - Every tag carries crossorigin="anonymous". Without it the browser cannot
//     read a cross-origin response well enough to hash it, so it skips the
//     check rather than failing — integrity silently off, page working
//     perfectly, right up until the day the CDN serves something else.
//
// Run `make web-sri` after changing a version; it refetches each URL and
// reports the hash to record here.
var (
	xtermJS = asset{
		URL:       "https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/lib/xterm.js",
		Integrity: "sha384-f/1U6Z9wM4D71a5eRXEZnyOTMOvjqxr2XLwh+Go1OvIl3L3tOcvUrzudnhbECwl4",
	}
	xtermCSS = asset{
		URL:       "https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/css/xterm.css",
		Integrity: "sha384-n2n7twoohnW+d3myBKaUgl7DSiwidw6MkQy9oesGzkPpMjejKRR3XlnD+5yCdtBD",
	}
	xtermFit = asset{
		URL:       "https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.11.0/lib/addon-fit.js",
		Integrity: "sha384-txoiwu4RR2GD3qySbaj+BbzibkLbSJRcfqGYMu6z1EqHil4A2dyBiBW5dlacG6OR",
	}
)

// assets is every one of them, for the checks that have to cover them all.
var assets = []asset{xtermJS, xtermCSS, xtermFit}

// asset is one file loaded from somewhere other than this binary, with the hash
// the browser must find it to have.
type asset struct {
	URL       string
	Integrity string
}

// origins is the set of hosts the assets come from, in the form a content
// security policy wants. Derived from the URLs rather than written out beside
// them, so that moving an asset to another host cannot leave the policy behind
// still naming the old one.
func origins() string {
	var seen []string
	for _, a := range assets {
		u, err := url.Parse(a.URL)
		if err != nil {
			continue
		}
		origin := u.Scheme + "://" + u.Host
		if !slices.Contains(seen, origin) {
			seen = append(seen, origin)
		}
	}
	return strings.Join(seen, " ")
}
