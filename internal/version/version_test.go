package version

import (
	"encoding/json"
	"os"
	"testing"
)

// TestManifestMatches keeps mygo.json, which `mygo build` reads, in step
// with the version compiled into the binaries.
func TestManifestMatches(t *testing.T) {
	data, err := os.ReadFile("../../mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != Version {
		t.Fatalf("mygo.json version %q, internal/version %q", m.Version, Version)
	}
}

func TestOlder(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.0", "0.1.1", true},
		{"0.2.0", "0.1.9", false},
		{"0.1.0", "0.1.0", false},
		{"v0.9.0", "0.10.0", true},
	}
	for _, c := range cases {
		if got := Older(c.a, c.b); got != c.want {
			t.Errorf("Older(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
