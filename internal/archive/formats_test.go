package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// tarBytes builds an uncompressed tar: app/bin/tool (executable) and app/VERSION.
func tarBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range []entry{exe("app/bin/tool"), file("app/VERSION", "v1")} {
		tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Size: int64(len(e.body))})
		tw.Write([]byte(e.body))
	}
	tw.Close()
	return buf.Bytes()
}

func compressWith(t *testing.T, compression string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch compression {
	case "":
		return data
	case "gzip":
		w := gzip.NewWriter(&buf)
		w.Write(data)
		w.Close()
	case "xz":
		w, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
		w.Close()
	case "zstd":
		w, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
		w.Close()
	case "bzip2":
		if _, err := exec.LookPath("bzip2"); err != nil {
			t.Skip("bzip2 not installed (Go has no bzip2 writer)")
		}
		cmd := exec.Command("bzip2", "-c")
		cmd.Stdin = bytes.NewReader(data)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	return buf.Bytes()
}

func writeFileT(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCompressedTars(t *testing.T) {
	for _, c := range []string{"", "gzip", "xz", "bzip2", "zstd"} {
		t.Run("tar+"+c, func(t *testing.T) {
			// The name is deliberately wrong: detection must use the content.
			src := writeFileT(t, "download", compressWith(t, c, tarBytes(t)))
			f, err := Detect(src)
			if err != nil || f != (Format{Compression: c, Kind: Tar}) {
				t.Fatalf("Detect = %+v, %v", f, err)
			}
			if err := Valid(src); err != nil {
				t.Errorf("Valid: %v", err)
			}
			dest := t.TempDir()
			if _, err := Extract(src, dest, "x"); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(dest, "app/bin/tool"))
			if err != nil || info.Mode().Perm()&0o111 == 0 {
				t.Errorf("tool: %v %v", info, err)
			}
		})
	}
}

func TestFormatNames(t *testing.T) {
	cases := map[Format][2]string{
		{Compression: "xz", Kind: Tar}:      {"tar.xz", ".tar.xz"},
		{Compression: "", Kind: Tar}:        {"tar", ".tar"},
		{Kind: Zip}:                         {"zip", ".zip"},
		{Kind: AppImage}:                    {"AppImage", ".AppImage"},
		{Kind: Binary}:                      {"binary", ""},
		{Compression: "gzip", Kind: Binary}: {"gzip-compressed binary", ".gz"},
	}
	for f, want := range cases {
		if f.String() != want[0] || f.Ext() != want[1] {
			t.Errorf("%+v: %s %s, want %v", f, f.String(), f.Ext(), want)
		}
	}
}

func zipBytes(t *testing.T, entries map[string]os.FileMode, symlinks map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, mode := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(mode)
		w, _ := zw.CreateHeader(h)
		if !mode.IsDir() {
			w.Write([]byte("content of " + name))
		}
	}
	for name, target := range symlinks {
		h := &zip.FileHeader{Name: name}
		h.SetMode(os.ModeSymlink | 0o777)
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(target))
	}
	zw.Close()
	return buf.Bytes()
}

func TestZip(t *testing.T) {
	src := writeFileT(t, "a.zip", zipBytes(t,
		map[string]os.FileMode{"app/": os.ModeDir | 0o755, "app/tool": 0o755, "app/readme.txt": 0o644, "app/nomode": 0},
		map[string]string{"app/alias": "tool"}))
	if f, _ := Detect(src); f.Kind != Zip {
		t.Fatalf("Detect = %+v", f)
	}
	if err := Valid(src); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := Extract(src, dest, "x"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dest, "app/tool")); info == nil || info.Mode().Perm()&0o111 == 0 {
		t.Error("executable bit lost")
	}
	if info, _ := os.Stat(filepath.Join(dest, "app/nomode")); info == nil || info.Mode().Perm() != 0o644 {
		t.Errorf("entry without Unix mode: %v", info)
	}
	if target, _ := os.Readlink(filepath.Join(dest, "app/alias")); target != "tool" {
		t.Errorf("symlink = %q", target)
	}
	if root, _ := InstallRoot(dest); root != filepath.Join(dest, "app") {
		t.Errorf("InstallRoot = %s", root)
	}
}

func TestZipTraversalRejected(t *testing.T) {
	outside := t.TempDir()
	src := writeFileT(t, "evil.zip", zipBytes(t, map[string]os.FileMode{"../../evil": 0o644, `..\..\evil2`: 0o644}, nil))
	dest := filepath.Join(outside, "dest")
	os.Mkdir(dest, 0o755)
	if _, err := Extract(src, dest, "x"); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "evil")); err == nil {
		t.Error("wrote outside the destination")
	}
}

func TestZipSymlinkEscapeBlocked(t *testing.T) {
	outside := t.TempDir()
	// The link entry comes first, then a file "inside" it.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, _ := zw.CreateHeader(h)
	w.Write([]byte(outside))
	w, _ = zw.Create("link/pwned")
	w.Write([]byte("x"))
	zw.Close()
	src := writeFileT(t, "evil.zip", buf.Bytes())
	if _, err := Extract(src, t.TempDir(), "x"); err == nil {
		t.Error("want error writing through an escaping symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Error("wrote through the symlink")
	}
}

func fakeELF(appImage bool) []byte {
	b := make([]byte, 4096)
	copy(b, magicELF)
	if appImage {
		copy(b[8:], []byte{'A', 'I', 2})
	}
	return b
}

func TestSingleFileDownloads(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want Format
	}{
		{"AppImage", fakeELF(true), Format{Kind: AppImage}},
		{"binary", fakeELF(false), Format{Kind: Binary}},
		{"gzip binary", nil, Format{Compression: "gzip", Kind: Binary}},
		{"xz binary", nil, Format{Compression: "xz", Kind: Binary}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := c.data
			if data == nil {
				data = compressWith(t, c.want.Compression, fakeELF(false))
			}
			src := writeFileT(t, "dl", data)
			f, err := Detect(src)
			if err != nil || f != c.want || !f.SingleFile() {
				t.Fatalf("Detect = %+v, %v", f, err)
			}
			dest := t.TempDir()
			if _, err := Extract(src, dest, "mytool"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dest, "mytool"))
			if err != nil || !bytes.HasPrefix(got, magicELF) {
				t.Errorf("mytool: %v", err)
			}
			if info, _ := os.Stat(filepath.Join(dest, "mytool")); info.Mode().Perm() != 0o755 {
				t.Errorf("mode = %v", info.Mode())
			}
			if _, err := Extract(src, t.TempDir(), "../escape"); err == nil {
				t.Error("unsafe single-file name accepted")
			}
		})
	}
}

func TestRealBinary(t *testing.T) {
	data, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Skip("no /usr/bin/true")
	}
	if f, err := Detect(writeFileT(t, "true", data)); err != nil || f.Kind != Binary {
		t.Errorf("Detect(/usr/bin/true) = %+v, %v", f, err)
	}
}

func TestUnsupported(t *testing.T) {
	for name, data := range map[string][]byte{
		"script":    []byte("#!/bin/sh\necho installer\n"),
		"empty":     nil,
		"gzip text": compressWith(t, "gzip", []byte(strings.Repeat("plain text ", 100))),
	} {
		src := writeFileT(t, "f", data)
		if _, err := Extract(src, t.TempDir(), "x"); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestHasArchiveExt(t *testing.T) {
	for _, n := range []string{"a.tar.gz", "a.tgz", "b.tar.xz", "c.tar.zst", "d.zip", "Some-1.0-x86_64.AppImage", "e.tar.bz2"} {
		if !HasArchiveExt(n) {
			t.Errorf("%s: want true", n)
		}
	}
	for _, n := range []string{"a.sha256", "a.tar.gz.asc", "readme.txt", "a.deb", "a.rpm"} {
		if HasArchiveExt(n) {
			t.Errorf("%s: want false", n)
		}
	}
}
