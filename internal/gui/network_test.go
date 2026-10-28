package gui

import (
	"slices"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// TestOptionsLost: switching back to Kyokai Network warns only about the
// networks that set EasyTier options; none set (a TOML of comments only)
// asks nothing.
func TestOptionsLost(t *testing.T) {
	plain := localapi.NetworkProfile{Name: "home", TOML: "# nothing set\n"}
	set := localapi.NetworkProfile{Name: "work", Label: "Office", Options: []string{"mtu", "flags.enable_kcp_proxy"}}
	cases := []struct {
		name     string
		profiles []localapi.NetworkProfile
		want     []string
	}{
		{"nothing changed", []localapi.NetworkProfile{plain, {Name: "other"}}, nil},
		{"one network set options", []localapi.NetworkProfile{plain, set}, []string{"Office: mtu, flags.enable_kcp_proxy"}},
		{"no networks", nil, nil},
	}
	for _, c := range cases {
		if got := optionsLost(c.profiles); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEngineChoices: Kyokai Network first, then the downloadable cores
// marked downloaded or not, then a downloaded one no longer listed, and the
// one in use even when nothing could be listed.
func TestEngineChoices(t *testing.T) {
	cores := &localapi.Cores{
		Installed: []string{"v2.6.3", "v2.7.0"},
		Releases:  []localapi.CoreRelease{{Tag: "v2.7.0", Prerelease: true}, {Tag: "v2.6.4"}},
	}
	got := engineChoices("v2.7.0", cores)
	want := []engineChoice{{installed: true}, {tag: "v2.7.0", pre: true, installed: true}, {tag: "v2.6.4"}, {tag: "v2.6.3", installed: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v, want %+v", got, want)
	}
	if got := engineChoices("v2.6.4", nil); !slices.Equal(got, []engineChoice{{installed: true}, {tag: "v2.6.4", installed: true}}) {
		t.Fatalf("unlisted: %+v", got)
	}
}
