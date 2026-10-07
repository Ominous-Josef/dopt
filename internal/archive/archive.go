// Package archive unpacks app downloads safely: every write is confined to the
// destination folder (no "../" entries, no writing through symlinks that point
// outside), hard links are kept, and setuid/setgid bits are dropped.
//
// The format is detected from the file's content, not its name: tar archives
// (plain, gzip, xz, bzip2 or zstd), zip archives, AppImages, and bare Linux
// executables (optionally compressed).
package archive

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ErrEmpty means the archive has no files.
var ErrEmpty = errors.New("the archive is empty")

// Kind is what a download contains.
type Kind int

const (
	Tar      Kind = iota // a tar archive
	Zip                  // a zip archive
	AppImage             // a self-contained AppImage executable
	Binary               // a single Linux executable
)

// Format is a download's compression and content.
type Format struct {
	Compression string // "", "gzip", "xz", "bzip2" or "zstd"
	Kind        Kind
}

var compressionExt = map[string]string{"": "", "gzip": ".gz", "xz": ".xz", "bzip2": ".bz2", "zstd": ".zst"}

// String names the format for messages: "tar.xz", "zip", "AppImage", "binary".
func (f Format) String() string {
	switch f.Kind {
	case Zip:
		return "zip"
	case AppImage:
		return "AppImage"
	case Binary:
		if f.Compression != "" {
			return f.Compression + "-compressed binary"
		}
		return "binary"
	}
	return "tar" + compressionExt[f.Compression]
}

// Ext is the usual file extension: ".tar.gz", ".zip", ".AppImage", ".xz", ...
func (f Format) Ext() string {
	switch f.Kind {
	case Zip:
		return ".zip"
	case AppImage:
		return ".AppImage"
	case Binary:
		return compressionExt[f.Compression]
	}
	return ".tar" + compressionExt[f.Compression]
}

// SingleFile reports whether the download is the app's executable itself.
func (f Format) SingleFile() bool { return f.Kind == AppImage || f.Kind == Binary }

// Extensions are the file name endings of supported downloads, for matching assets and listing files.
var Extensions = []string{".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.bz2", ".tbz2", ".tbz", ".tar.zst", ".tzst", ".tar", ".zip", ".AppImage", ".appimage"}

// HasArchiveExt reports whether name ends in one of Extensions.
func HasArchiveExt(name string) bool {
	for _, ext := range Extensions {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

var (
	magicGzip  = []byte{0x1f, 0x8b}
	magicXz    = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	magicBzip2 = []byte("BZh")
	magicZstd  = []byte{0x28, 0xb5, 0x2f, 0xfd}
	magicZip   = []byte("PK\x03\x04")
	magicZip0  = []byte("PK\x05\x06") // empty zip
	magicELF   = []byte("\x7fELF")
)

func compressionOf(head []byte) string {
	switch {
	case bytes.HasPrefix(head, magicGzip):
		return "gzip"
	case bytes.HasPrefix(head, magicXz):
		return "xz"
	case bytes.HasPrefix(head, magicBzip2):
		return "bzip2"
	case bytes.HasPrefix(head, magicZstd):
		return "zstd"
	}
	return ""
}

func decompress(r io.Reader, compression string) (io.ReadCloser, error) {
	switch compression {
	case "":
		return io.NopCloser(r), nil
	case "gzip":
		return gzip.NewReader(r)
	case "xz":
		x, err := xz.NewReader(r)
		return io.NopCloser(x), err
	case "bzip2":
		return io.NopCloser(bzip2.NewReader(r)), nil
	case "zstd":
		z, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return z.IOReadCloser(), nil
	}
	return nil, fmt.Errorf("unknown compression %q", compression)
}

// kindOf classifies (decompressed) content from its first bytes.
func kindOf(head []byte) (Kind, bool) {
	switch {
	case bytes.HasPrefix(head, magicELF):
		// AppImages are ELF files with "AI" and a type byte at offset 8.
		if len(head) >= 11 && head[8] == 'A' && head[9] == 'I' && (head[10] == 1 || head[10] == 2) {
			return AppImage, true
		}
		return Binary, true
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return Tar, true
	}
	return Tar, false
}

// Detect identifies the format of the file at p.
func Detect(p string) (Format, error) {
	f, err := os.Open(p)
	if err != nil {
		return Format{}, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1024)
	head, _ := br.Peek(512)
	if len(head) == 0 {
		return Format{}, errors.New("the file is empty")
	}
	if bytes.HasPrefix(head, magicZip) || bytes.HasPrefix(head, magicZip0) {
		return Format{Kind: Zip}, nil
	}
	c := compressionOf(head)
	if c == "" {
		if k, ok := kindOf(head); ok {
			return Format{Kind: k}, nil
		}
		return Format{}, errors.New("not a supported archive (expected tar, zip, AppImage or a Linux executable, optionally compressed with gzip, xz, bzip2 or zstd)")
	}
	rc, err := decompress(br, c)
	if err != nil {
		return Format{}, fmt.Errorf("reading %s data: %w", c, err)
	}
	defer rc.Close()
	inner := make([]byte, 512)
	n, _ := io.ReadFull(rc, inner)
	if k, ok := kindOf(inner[:n]); ok {
		return Format{Compression: c, Kind: k}, nil
	}
	// Old-style tar headers have no "ustar" marker; let the tar reader decide.
	return Format{Compression: c, Kind: Tar}, nil
}

// openStream returns the decompressed content of p.
func openStream(p string, f Format) (io.ReadCloser, error) {
	file, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	rc, err := decompress(bufio.NewReader(file), f.Compression)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("reading %s data: %w", f.Compression, err)
	}
	return struct {
		io.Reader
		io.Closer
	}{rc, closers{rc, file}}, nil
}

type closers []io.Closer

func (c closers) Close() error {
	var first error
	for _, cl := range c {
		if err := cl.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Valid reads the whole download and reports whether it is complete and readable.
func Valid(src string) error {
	f, err := Detect(src)
	if err != nil {
		return err
	}
	if f.Kind == Zip {
		zr, err := zip.OpenReader(src)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, zf := range zr.File {
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			_, err = io.Copy(io.Discard, rc)
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	rc, err := openStream(src, f)
	if err != nil {
		return err
	}
	defer rc.Close()
	if f.SingleFile() {
		_, err := io.Copy(io.Discard, rc)
		return err
	}
	tr := tar.NewReader(rc)
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

// entryName turns an archive entry name into a path relative to the destination.
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

// Extract unpacks src into the existing folder dest and returns its format.
// A single-file download (AppImage or binary) is written as dest/singleName.
func Extract(src, dest, singleName string) (Format, error) {
	f, err := Detect(src)
	if err != nil {
		return f, err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return f, err
	}
	defer root.Close()

	switch {
	case f.Kind == Zip:
		return f, extractZip(src, root)
	case f.SingleFile():
		return f, extractSingle(src, f, root, singleName)
	}
	rc, err := openStream(src, f)
	if err != nil {
		return f, err
	}
	defer rc.Close()
	return f, extractTar(tar.NewReader(rc), root)
}

func extractSingle(src string, f Format, root *os.Root, name string) error {
	if name == "" || !filepath.IsLocal(name) || strings.Contains(name, "/") {
		return fmt.Errorf("invalid file name %q for a single-file download", name)
	}
	rc, err := openStream(src, f)
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return fmt.Errorf("reading the download: %w", err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return root.Chmod(name, 0o755)
}

func extractTar(tr *tar.Reader, root *os.Root) error {
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
		perm := os.FileMode(hdr.Mode) & os.ModePerm
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = writeDir(root, name, perm)
		case tar.TypeReg:
			err = writeFile(root, name, perm, tr)
		case tar.TypeSymlink:
			err = writeSymlink(root, name, hdr.Linkname)
		case tar.TypeLink:
			err = writeHardLink(root, name, hdr.Linkname)
		}
		// Devices, FIFOs and other special files are never needed by an app: skipped.
		if err != nil {
			return fmt.Errorf("extracting %s: %w", hdr.Name, err)
		}
	}
}

func extractZip(src string, root *os.Root) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("reading zip: %w", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		name, err := entryName(strings.ReplaceAll(zf.Name, `\`, "/"))
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		mode := zf.Mode()
		perm := mode & os.ModePerm
		// Zips made on other systems often carry no Unix permissions.
		if perm == 0 {
			perm = 0o644
		}
		if err := extractZipEntry(root, zf, name, mode, perm); err != nil {
			return fmt.Errorf("extracting %s: %w", zf.Name, err)
		}
	}
	return nil
}

func extractZipEntry(root *os.Root, zf *zip.File, name string, mode, perm os.FileMode) error {
	if mode.IsDir() || strings.HasSuffix(zf.Name, "/") {
		return writeDir(root, name, perm|0o755)
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	if mode&os.ModeSymlink != 0 {
		target, err := io.ReadAll(io.LimitReader(rc, 4096))
		if err != nil {
			return err
		}
		return writeSymlink(root, name, string(target))
	}
	return writeFile(root, name, perm, rc)
}

// Owner always keeps write access, so later entries can land inside folders.
func writeDir(root *os.Root, name string, perm os.FileMode) error {
	if err := root.MkdirAll(name, 0o755); err != nil {
		return err
	}
	return root.Chmod(name, perm|0o700)
}

func writeFile(root *os.Root, name string, perm os.FileMode, r io.Reader) error {
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
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// OpenFile's mode is filtered by the umask; set the archive's bits (minus setuid/setgid/sticky).
	return root.Chmod(name, perm|0o600)
}

func writeSymlink(root *os.Root, name, target string) error {
	if err := mkParent(root, name); err != nil {
		return err
	}
	if err := removeIfPresent(root, name); err != nil {
		return err
	}
	// The link is stored as-is; os.Root stops any later entry from writing through it.
	return root.Symlink(target, name)
}

func writeHardLink(root *os.Root, name, linkname string) error {
	target, err := entryName(linkname)
	if err != nil || target == "" {
		return fmt.Errorf("unsafe hard link target %q", linkname)
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
