// Package archive unpacks app archives safely: every write is confined to the
// destination folder (no "../" entries, no writing through symlinks that point
// outside), hard links are kept, and setuid/setgid bits are dropped.
package archive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrEmpty means the archive has no files.
var ErrEmpty = errors.New("the archive is empty")

var gzipMagic = []byte{0x1f, 0x8b}

// open returns a tar reader over the gzip-compressed file at src.
func open(src string) (*tar.Reader, func() error, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(f)
	if magic, _ := br.Peek(2); !bytes.Equal(magic, gzipMagic) {
		f.Close()
		return nil, nil, errors.New("not a .tar.gz archive (no gzip header)")
	}
	gz, err := gzip.NewReader(br)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("reading gzip: %w", err)
	}
	return tar.NewReader(gz), func() error { gz.Close(); return f.Close() }, nil
}

// Valid reports whether src is a complete, readable .tar.gz.
func Valid(src string) error {
	tr, closeFn, err := open(src)
	if err != nil {
		return err
	}
	defer closeFn()
	for {
		_, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
	}
}

// entryName turns a tar entry name into a path relative to the destination.
// Leading "/" and "./" are dropped (as GNU tar does); anything that would
// leave the destination is rejected. It returns "" for the root entry itself.
func entryName(name string) (string, error) {
	clean := path.Clean(strings.TrimLeft(name, "/"))
	if clean == "." {
		return "", nil
	}
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("unsafe path in archive: %q", name)
	}
	return clean, nil
}

// Extract unpacks the .tar.gz at src into the existing folder dest.
func Extract(src, dest string) error {
	tr, closeFn, err := open(src)
	if err != nil {
		return err
	}
	defer closeFn()

	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		name, err := entryName(hdr.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if err := extractEntry(root, tr, hdr, name); err != nil {
			return fmt.Errorf("extracting %s: %w", hdr.Name, err)
		}
	}
}

func extractEntry(root *os.Root, tr *tar.Reader, hdr *tar.Header, name string) error {
	// Owner always keeps write access, so later entries can land inside folders.
	perm := os.FileMode(hdr.Mode) & os.ModePerm
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(name, 0o755); err != nil {
			return err
		}
		return root.Chmod(name, perm|0o700)
	case tar.TypeReg:
		if err := mkParent(root, name); err != nil {
			return err
		}
		// Replace whatever was there (an earlier entry, or a symlink) instead of writing through it.
		if err := removeIfPresent(root, name); err != nil {
			return err
		}
		f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm|0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		// OpenFile's mode is filtered by the umask; set the archive's bits (minus setuid/setgid/sticky).
		return root.Chmod(name, perm|0o600)
	case tar.TypeSymlink:
		if err := mkParent(root, name); err != nil {
			return err
		}
		if err := removeIfPresent(root, name); err != nil {
			return err
		}
		// The link is stored as-is; os.Root stops any later entry from writing through it.
		return root.Symlink(hdr.Linkname, name)
	case tar.TypeLink:
		target, err := entryName(hdr.Linkname)
		if err != nil || target == "" {
			return fmt.Errorf("unsafe hard link target %q", hdr.Linkname)
		}
		if target == name {
			return nil
		}
		if err := mkParent(root, name); err != nil {
			return err
		}
		if err := removeIfPresent(root, name); err != nil {
			return err
		}
		return root.Link(target, name)
	}
	// Devices, FIFOs and other special files are never needed by an app: skip them.
	return nil
}

func mkParent(root *os.Root, name string) error {
	if dir := path.Dir(name); dir != "." {
		return root.MkdirAll(dir, 0o755)
	}
	return nil
}

func removeIfPresent(root *os.Root, name string) error {
	if _, err := root.Lstat(name); err == nil {
		return root.RemoveAll(name)
	}
	return nil
}

// InstallRoot picks what to install from an extracted archive: if it holds a
// single top-level folder (app-1.2/...), that folder's contents; otherwise everything.
func InstallRoot(extracted string) (string, error) {
	entries, err := os.ReadDir(extracted)
	if err != nil {
		return "", err
	}
	switch {
	case len(entries) == 0:
		return "", ErrEmpty
	case len(entries) == 1 && entries[0].IsDir():
		// DirEntry.IsDir is false for a symlink to a folder, which is what we want.
		return filepath.Join(extracted, entries[0].Name()), nil
	}
	return extracted, nil
}
