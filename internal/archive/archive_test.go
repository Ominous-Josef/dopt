package archive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name     string
	typ      byte
	body     string
	linkname string
	mode     int64
}

func file(name, body string) entry {
	return entry{name: name, typ: tar.TypeReg, body: body, mode: 0o644}
}
func exe(name string) entry {
	return entry{name: name, typ: tar.TypeReg, body: "#!/bin/sh\n", mode: 0o755}
}
func dir(name string) entry { return entry{name: name, typ: tar.TypeDir, mode: 0o755} }

// writeTarGz builds a .tar.gz from entries and returns its path.
func writeTarGz(t *testing.T, entries ...entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.linkname, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
	return p
}

func extract(t *testing.T, src string) (string, error) {
	t.Helper()
	dest := t.TempDir()
	_, err := Extract(src, dest, "app")
	return dest, err
}

// The four layouts used by dopt-bash's tests/run.sh.
func TestLayouts(t *testing.T) {
	cases := []struct {
		name     string
		entries  []entry
		wantRoot string // relative to the extract folder
		wantFile string // relative to the install root
	}{
		{"wrap", []entry{dir("app/"), dir("app/bin/"), exe("app/bin/tool"), file("app/VERSION", "v1")}, "app", "bin/tool"},
		{"loose", []entry{dir("bin/"), exe("bin/tool"), dir("lib/"), file("lib/x.txt", "x"), file("VERSION", "v1")}, ".", "lib/x.txt"},
		{"files", []entry{exe("tool"), file("VERSION", "v1")}, ".", "tool"},
		{"deep", []entry{exe("app/opt/bin/tool"), file("app/VERSION", "v1")}, "app", "opt/bin/tool"},
		{"dot-prefixed", []entry{dir("./"), dir("./app/"), exe("./app/tool")}, "app", "tool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dest, err := extract(t, writeTarGz(t, c.entries...))
			if err != nil {
				t.Fatal(err)
			}
			root, err := InstallRoot(dest)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(dest, c.wantRoot); root != want {
				t.Errorf("InstallRoot = %s, want %s", root, want)
			}
			if _, err := os.Stat(filepath.Join(root, c.wantFile)); err != nil {
				t.Errorf("missing %s: %v", c.wantFile, err)
			}
		})
	}
}

func TestExecutableBitKept(t *testing.T) {
	dest, err := extract(t, writeTarGz(t, exe("tool")))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dest, "tool"))
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode = %v, want executable", info.Mode())
	}
}

func TestSetuidDropped(t *testing.T) {
	dest, err := extract(t, writeTarGz(t, entry{name: "tool", typ: tar.TypeReg, body: "x", mode: 0o4755 | 0o2000}))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dest, "tool"))
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Errorf("mode = %v, setuid/setgid kept", info.Mode())
	}
}

func TestTraversalRejected(t *testing.T) {
	for _, name := range []string{"../evil", "app/../../evil", ".."} {
		outside := t.TempDir()
		src := writeTarGz(t, file(name, "pwned"))
		dest := filepath.Join(outside, "dest")
		os.Mkdir(dest, 0o755)
		if _, err := Extract(src, dest, "app"); err == nil || !strings.Contains(err.Error(), "unsafe path") {
			t.Errorf("%s: err = %v, want unsafe path", name, err)
		}
		if _, err := os.Stat(filepath.Join(outside, "evil")); err == nil {
			t.Errorf("%s: wrote outside the destination", name)
		}
	}
}

func TestAbsolutePathStaysInside(t *testing.T) {
	dest, err := extract(t, writeTarGz(t, file("/etc/dopt-test", "x")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "etc/dopt-test")); err != nil {
		t.Errorf("absolute entry not placed inside: %v", err)
	}
}

func TestWriteThroughEscapingSymlinkBlocked(t *testing.T) {
	outside := t.TempDir()
	src := writeTarGz(t,
		entry{name: "link", typ: tar.TypeSymlink, linkname: outside},
		file("link/pwned", "x"),
	)
	if _, err := extract(t, src); err == nil {
		t.Error("want error writing through a symlink that leaves the destination")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Error("wrote through the symlink")
	}
}

func TestFileReplacesSymlinkInsteadOfFollowingIt(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "victim")
	os.WriteFile(target, []byte("original"), 0o644)
	src := writeTarGz(t,
		entry{name: "f", typ: tar.TypeSymlink, linkname: target},
		file("f", "new"),
	)
	dest, err := extract(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "original" {
		t.Error("file entry wrote through an earlier symlink")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "f")); string(b) != "new" {
		t.Errorf("f = %q", b)
	}
}

func TestInternalSymlinkKept(t *testing.T) {
	dest, err := extract(t, writeTarGz(t, exe("bin/real"), entry{name: "bin/alias", typ: tar.TypeSymlink, linkname: "real"}))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(dest, "bin/alias")); got != "real" {
		t.Errorf("symlink = %q", got)
	}
}

func TestHardLink(t *testing.T) {
	dest, err := extract(t, writeTarGz(t, file("a", "same"), entry{name: "dir/b", typ: tar.TypeLink, linkname: "a"}))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "dir/b")); err != nil || string(b) != "same" {
		t.Errorf("hard link: %q, %v", b, err)
	}
	if _, err := extract(t, writeTarGz(t, entry{name: "b", typ: tar.TypeLink, linkname: "../../etc/passwd"})); err == nil {
		t.Error("hard link out of the destination: want error")
	}
}

func TestEmptyArchive(t *testing.T) {
	dest, err := extract(t, writeTarGz(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallRoot(dest); !errors.Is(err, ErrEmpty) {
		t.Errorf("InstallRoot = %v, want ErrEmpty", err)
	}
}

func TestNotGzipAndCorrupt(t *testing.T) {
	junk := filepath.Join(t.TempDir(), "junk.tar.gz")
	os.WriteFile(junk, []byte("this is not an archive"), 0o644)
	if err := Valid(junk); err == nil {
		t.Error("Valid(junk) = nil")
	}
	if _, err := extract(t, junk); err == nil || !strings.Contains(err.Error(), "not a supported archive") {
		t.Errorf("extract junk: %v", err)
	}

	good := writeTarGz(t, file("a", strings.Repeat("x", 100000)))
	if err := Valid(good); err != nil {
		t.Errorf("Valid(good) = %v", err)
	}
	data, _ := os.ReadFile(good)
	truncated := filepath.Join(t.TempDir(), "cut.tar.gz")
	os.WriteFile(truncated, data[:len(data)/2], 0o644)
	if err := Valid(truncated); err == nil {
		t.Error("Valid(truncated) = nil")
	}
}
