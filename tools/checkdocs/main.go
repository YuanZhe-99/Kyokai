// Command checkdocs verifies that every language tree under doc/ mirrors
// doc/en-us: the same files, the same number of headings and table rows,
// and byte-identical fenced code blocks. Run from the repository root:
//
//	go run ./tools/checkdocs
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const source = "doc/en-us"

// shape is the structure two mirrored documents must share.
type shape struct {
	headings  int
	tableRows int
	code      []string
}

func main() {
	langs, err := os.ReadDir("doc")
	if err != nil {
		fail(err)
	}
	want, err := files(source)
	if err != nil {
		fail(err)
	}
	var problems []string
	for _, l := range langs {
		dir := filepath.Join("doc", l.Name())
		if !l.IsDir() || dir == source {
			continue
		}
		got, err := files(dir)
		if err != nil {
			fail(err)
		}
		for _, f := range want {
			if !slices.Contains(got, f) {
				problems = append(problems, fmt.Sprintf("%s: missing %s", dir, f))
			}
		}
		for _, f := range got {
			if !slices.Contains(want, f) {
				problems = append(problems, fmt.Sprintf("%s: %s has no %s counterpart", dir, f, source))
			}
		}
		for _, f := range want {
			if !slices.Contains(got, f) {
				continue
			}
			a, b := read(filepath.Join(source, f)), read(filepath.Join(dir, f))
			if a.headings != b.headings {
				problems = append(problems, fmt.Sprintf("%s/%s: %d headings, %s has %d", dir, f, b.headings, source, a.headings))
			}
			if a.tableRows != b.tableRows {
				problems = append(problems, fmt.Sprintf("%s/%s: %d table rows, %s has %d", dir, f, b.tableRows, source, a.tableRows))
			}
			if !slices.Equal(a.code, b.code) {
				problems = append(problems, fmt.Sprintf("%s/%s: code blocks differ from %s", dir, f, source))
			}
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		os.Exit(1)
	}
	fmt.Println("docs mirror: ok")
}

// files lists the Markdown files under dir, relative to it.
func files(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	})
	slices.Sort(out)
	return out, err
}

// read extracts a document's shape: heading and table-row counts outside
// code, and the code blocks verbatim.
func read(path string) shape {
	data, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	var s shape
	var block []string
	inCode := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "```") {
			if inCode {
				s.code = append(s.code, strings.Join(block, "\n"))
				block = nil
			}
			inCode = !inCode
			continue
		}
		switch {
		case inCode:
			block = append(block, line)
		case strings.HasPrefix(line, "#"):
			s.headings++
		case strings.HasPrefix(line, "|"):
			s.tableRows++
		}
	}
	return s
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "checkdocs:", err)
	os.Exit(2)
}
