// Command deps compares the project's upstream pins with the latest upstream
// releases: the MyGo module (go.mod), the EasyTier release the engine embeds
// and the RustDesk release the vendored code comes from
// (third_party/upstream.json), and the Rust toolchain (rust-toolchain.toml).
//
//	go run ./tools/deps check [-strict]   compare pins with the latest releases
//	go run ./tools/deps update            move MyGo to its latest version
//
// EasyTier, RustDesk and Rust move by hand, each with its review and checks
// (doc/en-us/ci-cd.md). Run from the repository root.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/version"
)

const (
	pinsPath      = "third_party/upstream.json"
	toolchainPath = "rust-toolchain.toml"
	mygoModule    = "github.com/egoist/mygo"
)

// Pins are the upstream releases recorded in third_party/upstream.json.
type Pins struct {
	EasyTier Pin `json:"easytier"`
	RustDesk Pin `json:"rustdesk"`
}

// Pin is one upstream: its repository, the release used and that release's
// commit, which is what builds depend on (tags can move).
type Pin struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Commit     string `json:"commit"`
	License    string `json:"license"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = check(os.Args[2:])
	case "update":
		err = update()
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "deps:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run ./tools/deps check [-strict] | update")
	os.Exit(2)
}

// check prints the pinned and latest versions of every upstream.
// Side effects: network requests to the Go proxy and the GitHub API.
// Notes: with -strict a newer upstream release is an error (CI uses it).
// EasyTier counts every published release, prereleases included, except the
// moving "nightly" tag.
func check(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	strict := fs.Bool("strict", false, "fail when an upstream pin is outdated")
	fs.Parse(args)

	var pins Pins
	data, err := os.ReadFile(pinsPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &pins); err != nil {
		return fmt.Errorf("%s: %w", pinsPath, err)
	}

	outdated := false
	report := func(name, pinned, latest, note string) {
		state := "current"
		if version.Older(pinned, latest) {
			state = "UPDATE AVAILABLE"
			outdated = true
		}
		if note != "" {
			state += " (" + note + ")"
		}
		fmt.Printf("%-9s pinned %-10s latest %-10s %s\n", name, pinned, latest, state)
	}

	curMyGo, latestMyGo, err := mygoVersions()
	if err != nil {
		return err
	}
	report("MyGo", curMyGo, latestMyGo, "")

	newest, stable, err := easyTierReleases(pins.EasyTier.Repository)
	if err != nil {
		return err
	}
	note := "latest stable " + stable
	if pins.EasyTier.Prerelease {
		note = "pinned prerelease; " + note
	}
	report("EasyTier", pins.EasyTier.Tag, newest, note)

	rd, err := latestRelease(pins.RustDesk.Repository)
	if err != nil {
		return err
	}
	report("RustDesk", pins.RustDesk.Tag, rd, "vendored code: review the diff")

	rust, err := rustChannel()
	if err != nil {
		return err
	}
	latestRust, err := latestRelease("rust-lang/rust")
	if err != nil {
		return err
	}
	report("Rust", rust, latestRust, "")

	if outdated && *strict {
		return errors.New("upstream pins are outdated")
	}
	return nil
}

// update moves MyGo to its latest version.
// Side effects: runs `go get` and `go mod tidy`.
// Notes: review `go tool mygo migrate-ui .` afterwards (doc/en-us/ci-cd.md).
func update() error {
	for _, cmd := range [][]string{{"go", "get", mygoModule + "@latest"}, {"go", "mod", "tidy"}} {
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(cmd, " "), err)
		}
	}
	fmt.Println("MyGo moved; now run `go tool mygo migrate-ui .` and review it by hand.")
	return nil
}

// mygoVersions returns the pinned and the latest MyGo versions.
func mygoVersions() (current, latest string, err error) {
	out, err := exec.Command("go", "list", "-m", "-u", "-json", mygoModule).Output()
	if err != nil {
		return "", "", fmt.Errorf("go list %s: %w", mygoModule, err)
	}
	var mod struct {
		Version string
		Update  *struct{ Version string }
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		return "", "", err
	}
	latest = mod.Version
	if mod.Update != nil {
		latest = mod.Update.Version
	}
	return mod.Version, latest, nil
}

// easyTierReleases returns the newest published release (prereleases
// included, "nightly" excluded) and the newest stable one.
func easyTierReleases(repo string) (newest, stable string, err error) {
	var rels []struct {
		TagName    string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if err := githubGet("repos/"+repo+"/releases?per_page=30", &rels); err != nil {
		return "", "", err
	}
	for _, r := range rels {
		if r.Draft || r.TagName == "nightly" {
			continue
		}
		if newest == "" || version.Older(newest, r.TagName) {
			newest = r.TagName
		}
		if !r.Prerelease && (stable == "" || version.Older(stable, r.TagName)) {
			stable = r.TagName
		}
	}
	if newest == "" {
		return "", "", fmt.Errorf("%s has no releases", repo)
	}
	return newest, stable, nil
}

// latestRelease returns the tag of a repository's latest stable release.
func latestRelease(repo string) (string, error) {
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := githubGet("repos/"+repo+"/releases/latest", &r); err != nil {
		return "", err
	}
	return r.TagName, nil
}

var channelRE = regexp.MustCompile(`(?m)^channel\s*=\s*"([^"]+)"`)

// rustChannel returns the toolchain pinned in rust-toolchain.toml.
func rustChannel() (string, error) {
	data, err := os.ReadFile(toolchainPath)
	if err != nil {
		return "", err
	}
	m := channelRE.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("%s pins no channel", toolchainPath)
	}
	return string(m[1]), nil
}

var httpClient = &http.Client{Timeout: time.Minute}

// githubGet decodes the JSON of a GitHub API path into v, with GITHUB_TOKEN
// when set (CI) to lift the anonymous rate limit.
func githubGet(path string, v any) error {
	req, err := http.NewRequest("GET", "https://api.github.com/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
