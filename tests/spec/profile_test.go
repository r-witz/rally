package spec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProfile checks docs/PROFILE.md: every row of a behaviour table names
// at least one test, and every test named by a row that is not planned
// exists.
func TestProfile(t *testing.T) {
	profile, err := os.ReadFile("../../docs/PROFILE.md")
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]bool{}
	files, _ := filepath.Glob("../../*_test.go")
	more, _ := filepath.Glob("../../internal/*/*_test.go")
	bench, _ := filepath.Glob("../bench/*_test.go")
	here, _ := filepath.Glob("*_test.go")
	for _, f := range append(append(append(files, more...), bench...), here...) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`(?m)^func ((?:Test|Fuzz|Benchmark)\w+)\(`).FindAllStringSubmatch(string(src), -1) {
			defined[m[1]] = true
		}
	}
	row := regexp.MustCompile(`^\| (\d+\.\d+) \|`)
	name := regexp.MustCompile("`((?:Test|Fuzz|Benchmark)\\w+)`")
	rows := 0
	for _, line := range strings.Split(string(profile), "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows++
		cells := strings.Split(line, " | ")
		tests := name.FindAllStringSubmatch(cells[len(cells)-2], -1)
		if len(tests) == 0 {
			t.Errorf("row %s names no test", m[1])
		}
		planned := strings.Contains(cells[len(cells)-1], "planned")
		for _, test := range tests {
			if !defined[test[1]] && !planned {
				t.Errorf("row %s names %s, which does not exist", m[1], test[1])
			}
		}
		if !planned && !strings.Contains(cells[len(cells)-1], "✅") {
			t.Errorf("row %s is not marked as conforming or planned", m[1])
		}
	}
	if rows < 60 {
		t.Errorf("only %d rows found", rows)
	}
}
