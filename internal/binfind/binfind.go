// Package binfind locates an app's executable inside an install folder.
package binfind

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MaxDepth is how deep (in folders below the root) binaries are searched for.
const MaxDepth = 3

// Helper executables shipped by Electron/Chromium apps that are never the app itself.
var helpers = map[string]bool{"chrome-sandbox": true, "crashpad_handler": true, "chrome_crashpad_handler": true}

type file struct {
	rel   string
	depth int
	mode  fs.FileMode
}

// walk lists regular files (not symlinks) up to MaxDepth, shallowest first, then by path.
func walk(root string) []file {
	var files []file
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if d.IsDir() {
			if depth >= MaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, file{rel: rel, depth: depth, mode: info.Mode()})
		return nil
	})
	sort.SliceStable(files, func(i, j int) bool { return files[i].depth < files[j].depth })
	return files
}

// ByPattern returns the shallowest file whose name matches the glob pattern
// (case-insensitive), or "" if none does.
func ByPattern(root, pattern string) string {
	pattern = strings.ToLower(pattern)
	for _, f := range walk(root) {
		if ok, _ := path.Match(pattern, strings.ToLower(filepath.Base(f.rel))); ok {
			return filepath.Join(root, f.rel)
		}
	}
	return ""
}

// FirstExecutable returns an executable directly inside root that isn't a known helper.
func FirstExecutable(root string) string {
	for _, f := range walk(root) {
		if f.depth == 1 && f.mode&0o111 != 0 && !helpers[filepath.Base(f.rel)] {
			return filepath.Join(root, f.rel)
		}
	}
	return ""
}

// Candidates lists up to limit executables (relative paths, shallowest first),
// skipping shared libraries and helpers, for the user to pick from.
func Candidates(root string, limit int) []string {
	var out []string
	for _, f := range walk(root) {
		name := filepath.Base(f.rel)
		if f.mode&0o111 == 0 || helpers[name] || strings.HasSuffix(name, ".so") || strings.Contains(name, ".so.") {
			continue
		}
		out = append(out, f.rel)
		if len(out) == limit {
			break
		}
	}
	return out
}

// Inside reports whether p, with symlinks resolved, is a regular file inside root.
func Inside(p, root string) bool {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return strings.HasPrefix(real, realRoot+string(filepath.Separator))
}
