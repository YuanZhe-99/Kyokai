package gui

// Adapted from EasyTier MyGo's internal/gui/vendorlogo.go (same author,
// GPL-3.0, relicensed under AGPL-3.0 here): matching by what a device says
// about itself instead of by hostname.

import (
	"embed"
	"strings"
	"sync"
	"unicode"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// Vendor logos (sources and licenses: assets/logos/SOURCES.md;
// doc/en-us/gui.md, "Device names").
//
//go:embed assets/logos/*.svg
var logoFS embed.FS

// vendorKeywords maps words to logo files, in priority order: makers and
// product lines first, then the systems. The first entry with a matching
// word wins.
var vendorKeywords = []struct {
	logo  string
	words []string
}{
	{"apple", []string{"apple", "mac", "macbook", "macmini", "macpro", "macstudio", "imac"}},
	{"lenovo", []string{"lenovo", "thinkpad", "thinkcentre", "thinkstation", "ideapad", "legion", "yoga"}},
	{"dell", []string{"dell", "xps", "optiplex", "precision", "latitude", "inspiron", "alienware"}},
	{"hp", []string{"hp", "hewlett", "hewlettpackard", "elitebook", "probook", "elitedesk", "prodesk", "zbook", "omen", "pavilion", "envy", "spectre"}},
	{"asus", []string{"asus", "asustek", "rog", "zenbook", "vivobook"}},
	{"acer", []string{"acer", "aspire", "predator", "swift", "nitro"}},
	{"samsung", []string{"samsung"}},
	{"huawei", []string{"huawei", "matebook"}},
	{"xiaomi", []string{"xiaomi", "redmi", "timi"}},
	{"google", []string{"google", "pixelbook"}},
	{"sony", []string{"sony", "vaio"}},
	{"razer", []string{"razer"}},
	{"lg", []string{"lg"}},
	{"toshiba", []string{"toshiba", "dynabook"}},
	{"intel", []string{"intel", "nuc"}},
	{"qualcomm", []string{"qualcomm", "snapdragon"}},
	{"oracle", []string{"oracle", "innotek", "virtualbox"}},
	{"windows", []string{"windows", "win"}},
	{"macos", []string{"macos", "osx"}},
	{"linux", []string{"linux"}},
}

// logoCache holds parsed logos by file name; a nil entry records a file that
// failed to parse, so it is tried once.
var logoCache = struct {
	sync.Mutex
	m map[string]*ui.SVG
}{m: map[string]*ui.SVG{}}

// deviceLogoName returns the logo file (without ".svg") for a device, or ""
// when nothing matches.
// Notes: the maker is tried first, then the model, the friendly name and
// the system, so "ThinkPad" in a Lenovo's name never loses to its system.
// Only whole words match (see words), so "hp" does not match "php-dev".
func deviceLogoName(a localapi.About) string {
	for _, field := range []string{a.Maker, a.Model, a.Name, a.OS} {
		w := words(field)
		for _, k := range vendorKeywords {
			for _, kw := range k.words {
				if w[kw] {
					return k.logo
				}
			}
		}
	}
	return ""
}

// deviceLogo returns the parsed logo for a device, or nil when none matches.
// Side effects: parses the SVG on first use and caches it.
func deviceLogo(a localapi.About) *ui.SVG {
	name := deviceLogoName(a)
	if name == "" {
		return nil
	}
	return loadLogo(name)
}

// loadLogo parses an embedded logo once.
// Returns: nil for a missing or unparsable file.
func loadLogo(name string) *ui.SVG {
	logoCache.Lock()
	defer logoCache.Unlock()
	if s, ok := logoCache.m[name]; ok {
		return s
	}
	var s *ui.SVG
	if b, err := logoFS.ReadFile("assets/logos/" + name + ".svg"); err == nil {
		if p, err := ui.ParseSVG(b); err == nil {
			s = p
		}
	}
	logoCache.m[name] = s
	return s
}

// words splits a name into lower-case words for logo matching.
// Returns: a set holding each part between separators, each pair of
// neighboring parts joined ("tp-link" → "tplink"), and the pieces of a part
// split by splitCase ("NUC8i5BEH" → "nuc", "i", "beh"; "MacBookPro18,3" →
// "mac", "book", "pro").
func words(s string) map[string]bool {
	out := map[string]bool{}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for i, p := range parts {
		out[strings.ToLower(p)] = true
		if i > 0 {
			out[strings.ToLower(parts[i-1]+p)] = true
		}
		for _, x := range splitCase(p, false) {
			out[strings.ToLower(x)] = true
		}
		for _, x := range splitCase(p, true) {
			out[strings.ToLower(x)] = true
		}
	}
	return out
}

// splitCase splits a word where letters meet digits and, with camel set,
// also where a lower-case letter is followed by a capital ("iPhone15" →
// "iPhone", "15"; with camel → "i", "Phone", "15").
func splitCase(w string, camel bool) []string {
	var out []string
	rs := []rune(w)
	start := 0
	for i := 1; i < len(rs); i++ {
		a, b := rs[i-1], rs[i]
		if unicode.IsDigit(a) != unicode.IsDigit(b) || camel && unicode.IsLower(a) && unicode.IsUpper(b) {
			out = append(out, string(rs[start:i]))
			start = i
		}
	}
	return append(out, string(rs[start:]))
}

// deviceTitle is how a device is named in the interface: its friendly name
// with the grouped Device ID, or the Device ID alone.
func deviceTitle(a localapi.About, deviceID string) string {
	if a.Name == "" {
		return groupDigits(deviceID)
	}
	return a.Name + " · " + groupDigits(deviceID)
}

// deviceAvatar draws a device's round avatar: its vendor's logo, else the
// first letter of its name, else a dot.
func deviceAvatar(c *ui.Context, a localapi.About) {
	col := c.Theme().Accent
	ui.Box(c).Width(avatarSize).Height(avatarSize).Radius(avatarSize / 2).Background(col.Alpha(0.14)).Center().Children(func() {
		switch logo := deviceLogo(a); {
		case logo != nil:
			w, h := logo.Size()
			lw := logoSize
			if h > w {
				lw = logoSize * w / h
			}
			ui.Icon(c, logo).Width(lw).TextColor(col)
		case a.Name != "":
			ui.Text(c, strings.ToUpper(string([]rune(a.Name)[:1]))).TextColor(col).Bold()
		default:
			ui.Text(c, "●").TextColor(col)
		}
	})
}
