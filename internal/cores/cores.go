// Package cores downloads the official EasyTier cores a device may run its
// networks on instead of the embedded node (doc/en-us/networking.md,
// "Official EasyTier core"): the release list from GitHub, the asset for
// this system checked against the release's SHA-256 digest, unpacked into
// the state directory's cores folder, where the Rust core finds them by tag.
package cores

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Dir is the cores folder under the state directory (the Rust core's
// external::CORES_DIR); each core is a folder named by its tag.
const Dir = "cores"

// releasesURL lists EasyTier's releases, newest first.
const releasesURL = "https://api.github.com/repos/EasyTier/EasyTier/releases?per_page=30"

// maxAsset is the largest asset downloaded (a release zip is about 40 MB).
const maxAsset = 200 << 20

// supported are the tags Kyokai runs: the 2.6 and 2.7 lines, whose CLI
// lists peers the way the Rust core reads them (never "nightly").
var supported = regexp.MustCompile(`^v2\.[67]\.\d+$`)

// Release is a core that can be downloaded for this system.
type Release struct {
	Tag string `json:"tag"`
	// Pre: GitHub marks it a prerelease.
	Pre bool `json:"prerelease"`
	// URL and SHA256 are the asset's.
	URL    string `json:"-"`
	SHA256 string `json:"-"`
}

// ghRelease is a release as GitHub's API lists it.
type ghRelease struct {
	Tag    string `json:"tag_name"`
	Draft  bool   `json:"draft"`
	Pre    bool   `json:"prerelease"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

// AssetName is the zip EasyTier publishes for a system and architecture
// (Go's names), or "" where it publishes none Kyokai runs on.
func AssetName(goos, goarch, tag string) string {
	osName := map[string]string{"windows": "windows", "darwin": "macos"}[goos]
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	if goos == "windows" && goarch == "arm64" {
		arch = "arm64"
	}
	if osName == "" || arch == "" {
		return ""
	}
	return fmt.Sprintf("easytier-%s-%s-%s.zip", osName, arch, tag)
}

// pick keeps the releases Kyokai can run here: supported tags, no drafts,
// with this system's asset and its digest (a release without one is never
// offered: nothing could check it).
func pick(all []ghRelease, goos, goarch string) []Release {
	var out []Release
	for _, r := range all {
		if r.Draft || !supported.MatchString(r.Tag) {
			continue
		}
		name := AssetName(goos, goarch, r.Tag)
		for _, a := range r.Assets {
			sum, ok := strings.CutPrefix(a.Digest, "sha256:")
			if a.Name == name && ok && len(sum) == 64 {
				out = append(out, Release{Tag: r.Tag, Pre: r.Pre, URL: a.URL, SHA256: strings.ToLower(sum)})
			}
		}
	}
	return out
}

// Releases lists the cores that can be downloaded for this system, newest
// first.
// Side effects: one request to GitHub's API.
func Releases(ctx context.Context) ([]Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var all []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&all); err != nil {
		return nil, err
	}
	return pick(all, runtime.GOOS, runtime.GOARCH), nil
}

// Installed lists the cores downloaded into stateDir, by tag.
func Installed(stateDir string) []string {
	entries, _ := os.ReadDir(filepath.Join(stateDir, Dir))
	var tags []string
	for _, e := range entries {
		if e.IsDir() && supported.MatchString(e.Name()) {
			tags = append(tags, e.Name())
		}
	}
	slices.Sort(tags)
	return tags
}

// Install downloads a release's asset, checks it against its digest and
// unpacks the core, its CLI and their libraries into stateDir's cores
// folder under the tag, replacing what was there.
// Side effects: an HTTPS download; writes the folder atomically (a
// temporary folder renamed into place); nothing is kept when the digest
// does not match.
func Install(ctx context.Context, stateDir string, r Release) error {
	if !supported.MatchString(r.Tag) || r.URL == "" || len(r.SHA256) != 64 {
		return errors.New("not a release Kyokai runs")
	}
	data, err := download(ctx, r.URL)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != r.SHA256 {
		return errors.New("the download does not match the release's checksum")
	}
	dir := filepath.Join(stateDir, Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, ".install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := unpack(data, tmp); err != nil {
		return err
	}
	dst := filepath.Join(dir, r.Tag)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Remove deletes a downloaded core.
func Remove(stateDir, tag string) error {
	if !supported.MatchString(tag) {
		return errors.New("not a core")
	}
	return os.RemoveAll(filepath.Join(stateDir, Dir, tag))
}

// download reads a URL whole, at most maxAsset bytes.
func download(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the download answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAsset+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAsset {
		return nil, errors.New("the download is too large")
	}
	return data, nil
}

// wanted reports whether a file of the zip is one Kyokai keeps: the core,
// its CLI and the libraries next to them (not the web consoles or the
// driver).
func wanted(base string) bool {
	name := strings.TrimSuffix(base, ".exe")
	return name == "easytier-core" || name == "easytier-cli" || strings.HasSuffix(base, ".dll")
}

// unpack writes the zip's wanted files, flat, into dir: the programs
// executable by this user only.
func unpack(data []byte, dir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	found := 0
	for _, f := range zr.File {
		base := path.Base(f.Name)
		if f.FileInfo().IsDir() || !wanted(base) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxAsset))
		rc.Close()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, base), body, 0o700); err != nil {
			return err
		}
		if strings.HasPrefix(base, "easytier-") {
			found++
		}
	}
	if found < 2 {
		return errors.New("the release has no easytier-core and easytier-cli")
	}
	return nil
}
