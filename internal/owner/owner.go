// Package owner detects folders that belong to a system package (RPM or deb),
// which dopt must never modify.
package owner

import (
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
)

// Owner is the system package that owns a path.
type Owner struct {
	Package string // e.g. "golang"
	Tool    string // package manager to remove it with: "dnf" or "apt"
}

// Runner runs a command and returns its stdout; replaced in tests.
var Runner = func(name string, args ...string) ([]byte, bool) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, false
	}
	// rpm and dpkg exit non-zero when some paths are unowned but still print the owned ones.
	out, _ := exec.Command(name, args...).Output()
	return out, true
}

// sample is dir plus up to 20 files inside it (depth <= 3), which is enough to
// catch a package that owns the files but not the folder itself.
func sample(dir string) []string {
	paths := []string{dir}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != dir && strings.Count(strings.TrimPrefix(p, dir), "/") >= 3 {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			paths = append(paths, p)
			if len(paths) > 20 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return paths
}

// Find reports the package owning dir or a sample of the files inside it.
func Find(dir string) (Owner, bool) {
	paths := sample(dir)
	if out, ok := Runner("rpm", append([]string{"-qf", "--qf", "%{NAME}\n", "--"}, paths...)...); ok {
		// Owned paths print a bare package name; unowned ones print a sentence.
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.Contains(line, " ") {
				return Owner{Package: line, Tool: "dnf"}, true
			}
		}
	}
	if out, ok := Runner("dpkg", append([]string{"-S", "--"}, paths...)...); ok {
		// "pkg1, pkg2: /path" for owned paths; skip "diversion by ..." lines.
		for _, line := range strings.Split(string(out), "\n") {
			if line == "" || strings.HasPrefix(line, "diversion") {
				continue
			}
			pkgs, _, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			pkg, _, _ := strings.Cut(pkgs, ",")
			pkg, _, _ = strings.Cut(strings.TrimSpace(pkg), ":") // drop ":amd64"
			if pkg != "" {
				return Owner{Package: pkg, Tool: "apt"}, true
			}
		}
	}
	return Owner{}, false
}
