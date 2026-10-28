package cores

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestAssetName: each supported system and architecture names EasyTier's
// zip; others name none.
func TestAssetName(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"windows", "amd64", "easytier-windows-x86_64-v2.7.0.zip"},
		{"windows", "arm64", "easytier-windows-arm64-v2.7.0.zip"},
		{"darwin", "arm64", "easytier-macos-aarch64-v2.7.0.zip"},
		{"darwin", "amd64", "easytier-macos-x86_64-v2.7.0.zip"},
		{"linux", "amd64", ""},
		{"windows", "386", ""},
	}
	for _, c := range cases {
		if got := AssetName(c.goos, c.goarch, "v2.7.0"); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

// TestPick: only supported tags with this system's asset and a SHA-256
// digest are offered; drafts and nightly never.
func TestPick(t *testing.T) {
	sum := "sha256:65f6ed5f6f747b6ae74a07f55b84cf78127a89b618249626c98f8e82bd35d762"
	asset := func(tag, digest string) ghRelease {
		r := ghRelease{Tag: tag}
		r.Assets = append(r.Assets, struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		}{AssetName("windows", "arm64", tag), "https://example.invalid/" + tag, digest})
		return r
	}
	draft := asset("v2.6.5", sum)
	draft.Draft = true
	pre := asset("v2.7.0", sum)
	pre.Pre = true
	all := []ghRelease{asset("nightly", sum), pre, asset("v2.6.4", sum), asset("v2.6.3", ""), draft, asset("v2.5.0", sum), asset("v2.6.2", "sha1:abc")}
	got := pick(all, "windows", "arm64")
	if len(got) != 2 || got[0].Tag != "v2.7.0" || !got[0].Pre || got[1].Tag != "v2.6.4" || got[1].Pre {
		t.Fatalf("picked %+v", got)
	}
	if got[0].SHA256 != sum[len("sha256:"):] {
		t.Fatalf("digest %q", got[0].SHA256)
	}
	if len(pick(all, "linux", "amd64")) != 0 {
		t.Fatal("offered a core for an unsupported system")
	}
}

// TestUnpack: the core, the CLI and the libraries are kept, flat; the web
// consoles are not; a zip without both programs is refused.
func TestUnpack(t *testing.T) {
	build := func(names ...string) []byte {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		for _, n := range names {
			f, _ := w.Create(n)
			f.Write([]byte(n))
		}
		w.Close()
		return b.Bytes()
	}
	dir := t.TempDir()
	if err := unpack(build("e/easytier-core.exe", "e/easytier-cli.exe", "e/easytier-web.exe", "e/Packet.dll", "e/WinDivert64.sys"), dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"Packet.dll", "easytier-cli.exe", "easytier-core.exe"}; len(names) != 3 || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("kept %v", names)
	}
	if err := unpack(build("e/easytier-core"), t.TempDir()); err == nil {
		t.Fatal("a zip without the CLI was accepted")
	}
}

// TestInstallRefusesBadRelease: a release Kyokai does not run, or one without
// its asset and digest, is refused before any download and leaves nothing.
func TestInstallRefusesBadRelease(t *testing.T) {
	dir := t.TempDir()
	for _, r := range []Release{{Tag: "nightly", URL: "x", SHA256: string(make([]byte, 64))}, {Tag: "v2.7.0"}} {
		if err := Install(t.Context(), dir, r); err == nil {
			t.Fatalf("installed %+v", r)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, Dir, "v2.7.0")); !os.IsNotExist(err) {
		t.Fatal("left a folder")
	}
}
