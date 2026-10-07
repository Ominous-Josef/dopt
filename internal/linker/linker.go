// Package linker manages an app's command link (bin/<name> -> binary) without
// ever replacing a file dopt didn't create.
package linker

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Ominous-Josef/dopt/internal/sysuser"
)

// State of a command link path.
type State int

const (
	Free    State = iota // nothing there
	Ours                 // a symlink into this app's install folder
	Foreign              // anything else: never touch it
)

// Inspect reports whether link is free, ours (points into installDir), or foreign.
func Inspect(link, installDir string) State {
	info, err := os.Lstat(link)
	if errors.Is(err, fs.ErrNotExist) {
		return Free
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return Foreign
	}
	target, err := os.Readlink(link)
	if err != nil {
		return Foreign
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(link), target)
	}
	if PathInside(target, installDir) {
		return Ours
	}
	return Foreign
}

// PathInside reports whether p resolves (as far as it exists) to a path inside dir.
func PathInside(p, dir string) bool {
	rp, rd := Resolve(p), Resolve(dir)
	return strings.HasPrefix(rp, rd+string(filepath.Separator))
}

// Resolve is `readlink -m`: resolve symlinks in the existing part of the path, keep the rest.
func Resolve(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(Resolve(parent), filepath.Base(p))
}

// Link points link at target, replacing an existing link atomically.
// Callers must check Inspect first: Link replaces whatever is at link.
func Link(target, link string) error {
	tmp := filepath.Join(filepath.Dir(link), "."+filepath.Base(link)+".dopt-tmp")
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// RemoveIfOurs deletes link only if it points into installDir.
func RemoveIfOurs(link, installDir string) (bool, error) {
	if Inspect(link, installDir) != Ours {
		return false, nil
	}
	return true, os.Remove(link)
}

// Lookup finds name on the user's PATH the way their shell would. As root it
// asks a login shell of the real user, so the user's own PATH is used.
var Lookup = func(name string, u sysuser.User, asRoot bool) string {
	if !asRoot {
		p, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	out, err := exec.Command("sudo", "-u", u.Name, "bash", "-lc", `command -v -- "$1" || true`, "_", name).Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(last, "/") {
		return "" // a builtin, alias or function, not a file
	}
	return last
}

// OnPath reports whether dir is one of the entries in pathEnv.
func OnPath(dir, pathEnv string) bool {
	for _, d := range filepath.SplitList(pathEnv) {
		if d != "" && filepath.Clean(d) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}
