// Package desktop writes .desktop launchers and finds app icons.
package desktop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Text makes s safe as a Desktop Entry string value: one line (control
// characters become spaces, so no extra keys can be injected) with backslashes escaped.
func Text(s string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		if r == ' ' && lastSpace {
			continue
		}
		lastSpace = r == ' '
		b.WriteRune(r)
	}
	return strings.ReplaceAll(b.String(), `\`, `\\`)
}

// ExecPath quotes a program path for the Exec key ('%' escaped). Paths with
// characters that would need shell escaping inside quotes are refused.
func ExecPath(p string) (string, error) {
	if strings.ContainsAny(p, "\"`$\\") || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", fmt.Errorf("the command path %s contains characters (\" ` $ \\) that can't be used in a desktop shortcut", p)
	}
	return `"` + strings.ReplaceAll(p, "%", "%%") + `"`, nil
}

// Entry is the content of a launcher.
type Entry struct {
	Name       string
	Comment    string
	Command    string // the command link the launcher runs
	Flags      string // extra arguments, may include field codes like %U
	Icon       string // icon path or theme name; "" means system-run
	Categories string
	WMClass    string
}

// Render produces the .desktop file content.
func Render(e Entry) (string, error) {
	execPath, err := ExecPath(e.Command)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("[Desktop Entry]\nVersion=1.0\nType=Application\n")
	fmt.Fprintf(&b, "Name=%s\n", Text(e.Name))
	if c := Text(e.Comment); c != "" {
		fmt.Fprintf(&b, "Comment=%s\n", c)
	}
	execLine := execPath
	if f := Text(e.Flags); f != "" {
		execLine += " " + f
	}
	fmt.Fprintf(&b, "Exec=%s\n", execLine)
	icon := e.Icon
	if icon == "" {
		icon = "system-run"
	}
	fmt.Fprintf(&b, "Icon=%s\n", Text(icon))
	b.WriteString("Terminal=false\n")
	categories := e.Categories
	if categories == "" {
		categories = "Utility;"
	}
	fmt.Fprintf(&b, "Categories=%s\n", Text(categories))
	fmt.Fprintf(&b, "StartupWMClass=%s\n", Text(e.WMClass))
	return b.String(), nil
}

// Write renders e into file, replacing it atomically.
func Write(file string, e Entry) error {
	content, err := Render(e)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// Validate runs desktop-file-validate if installed and returns its complaints ("" if none or not installed).
func Validate(file string) string {
	if _, err := exec.LookPath("desktop-file-validate"); err != nil {
		return ""
	}
	out, _ := exec.Command("desktop-file-validate", file).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// Folders skipped when guessing an icon (glob patterns on the folder name).
var noisyDirs = []string{"node_modules", ".*", "locales", "test*"}

type found struct {
	path  string
	depth int
}

// search walks root up to maxDepth (0 = unlimited) and returns matching regular
// files, shallowest first. Folders matching skip are not entered.
func search(root string, maxDepth int, skip []string, match func(name string) bool) []string {
	var hits []found
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if d.IsDir() {
			for _, pat := range skip {
				if ok, _ := path.Match(pat, d.Name()); ok {
					return filepath.SkipDir
				}
			}
			if maxDepth > 0 && depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && match(d.Name()) {
			hits = append(hits, found{p, depth})
		}
		return nil
	})
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].depth < hits[j].depth })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.path
	}
	return out
}

// FindIcon picks the launcher icon inside installDir:
//  1. hint (manifest icon_path): a path relative to installDir, or a file name to search for;
//  2. icon.png/svg, <appID>.png/svg or <command>.png/svg (up to 8 levels deep);
//  3. any .png/.svg up to 5 levels deep, skipping node_modules, hidden, locales and test folders.
//
// It returns "" when nothing fits.
func FindIcon(installDir, hint, appID, command string) string {
	if hint != "" {
		if p := filepath.Join(installDir, hint); filepath.IsLocal(hint) {
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				return p
			}
		}
		base := strings.ToLower(filepath.Base(hint))
		if hits := search(installDir, 0, nil, func(n string) bool { return strings.ToLower(n) == base }); len(hits) > 0 {
			return hits[0]
		}
	}
	wanted := map[string]bool{}
	for _, stem := range []string{"icon", appID, command} {
		wanted[stem+".png"], wanted[stem+".svg"] = true, true
	}
	if hits := search(installDir, 8, nil, func(n string) bool { return wanted[n] }); len(hits) > 0 {
		return hits[0]
	}
	if hits := search(installDir, 5, noisyDirs, func(n string) bool {
		return strings.HasSuffix(n, ".png") || strings.HasSuffix(n, ".svg")
	}); len(hits) > 0 {
		return hits[0]
	}
	return ""
}

// Remove deletes a launcher; a missing file is not an error.
func Remove(file string) (bool, error) {
	err := os.Remove(file)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
