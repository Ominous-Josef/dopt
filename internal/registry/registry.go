// Package registry records which app folders dopt installed: one small
// key=value file per app in <opt dir>/.dopt/<app_id>, kept outside the app
// folders. The format is shared with dopt-bash, which reads app_id, folder_id
// and installed and ignores other keys.
package registry

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Ominous-Josef/dopt/internal/names"
)

// Keys written by dopt.
const (
	KeyAppID     = "app_id"
	KeyFolderID  = "folder_id"
	KeyInstalled = "installed"
	KeyBinary    = "binary"  // binary path relative to the install folder
	KeyCommand   = "command" // command link name
	KeySource    = "source"  // download URL or local archive path
)

// FolderIdentity is the folder's inode and birth time ("ino:btime", as `stat -c '%i:%W'`).
// It survives renames but changes when the folder is deleted and recreated.
// The birth time is 0 when the filesystem doesn't record it.
func FolderIdentity(path string) (string, error) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_INO|unix.STATX_BTIME, &st); err != nil {
		return "", err
	}
	var btime int64
	if st.Mask&unix.STATX_BTIME != 0 {
		btime = st.Btime.Sec
	}
	return fmt.Sprintf("%d:%d", st.Ino, btime), nil
}

// Entry is one app's registry record.
type Entry map[string]string

// Read returns appID's entry, or nil if there is none. For repeated keys the first wins.
func Read(dir, appID string) (Entry, error) {
	if !names.Valid(appID) {
		return nil, fmt.Errorf("invalid app ID %q", appID)
	}
	f, err := os.Open(filepath.Join(dir, appID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	e := Entry{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || k == "" {
			continue
		}
		if _, seen := e[k]; !seen {
			e[k] = v
		}
	}
	return e, sc.Err()
}

// Status of an install folder relative to the registry.
type Status int

const (
	Absent       Status = iota // no install folder
	Registered                 // installed by dopt and unchanged since
	Recreated                  // registered, but the folder was replaced since
	Unregistered               // a folder dopt has no record of
)

// Check reports whether installDir is the folder dopt registered for appID.
func Check(dir, appID, installDir string) (Status, error) {
	if _, err := os.Lstat(installDir); errors.Is(err, fs.ErrNotExist) {
		return Absent, nil
	} else if err != nil {
		return Absent, err
	}
	e, err := Read(dir, appID)
	if err != nil {
		return Unregistered, err
	}
	recorded := e[KeyFolderID]
	if recorded == "" {
		return Unregistered, nil
	}
	current, err := FolderIdentity(installDir)
	if err != nil {
		return Unregistered, err
	}
	if current != recorded {
		return Recreated, nil
	}
	return Registered, nil
}

// Write replaces appID's entry. app_id, folder_id and installed come first,
// in dopt-bash's order; other keys follow sorted. Values must be single-line.
func Write(dir, appID string, e Entry) error {
	if !names.Valid(appID) {
		return fmt.Errorf("invalid app ID %q", appID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	order := []string{KeyAppID, KeyFolderID, KeyInstalled}
	var rest []string
	for k := range e {
		if k != KeyAppID && k != KeyFolderID && k != KeyInstalled {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range append(order, rest...) {
		v, ok := e[k]
		if !ok {
			continue
		}
		if strings.ContainsAny(k, "=\n") || strings.ContainsAny(v, "\n\r") {
			return fmt.Errorf("invalid registry field %q", k)
		}
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	tmp, err := os.CreateTemp(dir, "."+appID+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
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
	return os.Rename(tmp.Name(), filepath.Join(dir, appID))
}

// Remove deletes appID's entry; a missing entry is not an error.
func Remove(dir, appID string) error {
	if !names.Valid(appID) {
		return fmt.Errorf("invalid app ID %q", appID)
	}
	err := os.Remove(filepath.Join(dir, appID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// List returns the registered app IDs, sorted.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.Type().IsRegular() && names.Valid(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}
