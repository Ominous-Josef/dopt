package install

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// moveTree moves the folder src to dst (which must not exist): a rename when
// both are on the same filesystem, otherwise a copy that keeps modes and symlinks.
func moveTree(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyTree(src, dst)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.Mkdir(target, 0o755); err != nil && !(rel == "." && errors.Is(err, fs.ErrExist)) {
				return err
			}
			return os.Chmod(target, info.Mode().Perm()|0o700)
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		}
		return nil // special files were never extracted
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

// dirSize is the disk usage of a folder (like du), counting hard links once.
func dirSize(dir string) int64 {
	var total int64
	seen := map[uint64]bool{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			if st.Nlink > 1 && !d.IsDir() {
				if seen[st.Ino] {
					return nil
				}
				seen[st.Ino] = true
			}
			total += st.Blocks * 512
		} else {
			total += info.Size()
		}
		return nil
	})
	return total
}

// humanSize formats bytes like `du -h`: 512B, 4.0K, 12M, 1.5G.
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	units := "KMGTP"
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v < 10 {
		return fmt.Sprintf("%.1f%c", v, units[i])
	}
	return fmt.Sprintf("%.0f%c", v, units[i])
}

var plainArg = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

// shellQuote quotes s for pasting into a shell.
func shellQuote(s string) string {
	if plainArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// redactURL hides a URL's query string, which may contain tokens.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" || u.Scheme == "" {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String() + "?…"
}
