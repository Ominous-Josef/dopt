package binfind

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func mk(t *testing.T, root string, files map[string]os.FileMode) {
	t.Helper()
	for rel, mode := range files {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode)
	}
}

func TestByPattern(t *testing.T) {
	root := t.TempDir()
	mk(t, root, map[string]os.FileMode{
		"a/b/tool":        0o755,
		"bin/Tool":        0o755,
		"x/y/z/deep-tool": 0o755,
	})
	if got := ByPattern(root, "tool"); got != filepath.Join(root, "bin/Tool") {
		t.Errorf("shallowest case-insensitive match = %s", got)
	}
	if got := ByPattern(root, "*-tool"); got != "" {
		t.Errorf("depth 4 should not be searched, got %s", got)
	}
	mk(t, root, map[string]os.FileMode{"opt/bin/three": 0o755})
	if got := ByPattern(root, "three"); got == "" {
		t.Error("depth 3 should be searched")
	}
}

func TestFirstExecutable(t *testing.T) {
	root := t.TempDir()
	mk(t, root, map[string]os.FileMode{"chrome-sandbox": 0o4755, "README": 0o644, "app": 0o755, "bin/other": 0o755})
	if got := FirstExecutable(root); got != filepath.Join(root, "app") {
		t.Errorf("FirstExecutable = %s", got)
	}
}

func TestCandidates(t *testing.T) {
	root := t.TempDir()
	mk(t, root, map[string]os.FileMode{
		"app":               0o755,
		"libfoo.so":         0o755,
		"libbar.so.1":       0o755,
		"crashpad_handler":  0o755,
		"bin/helper":        0o755,
		"resources/data.js": 0o644,
	})
	want := []string{"app", "bin/helper"}
	if got := Candidates(root, 10); !reflect.DeepEqual(got, want) {
		t.Errorf("Candidates = %v, want %v", got, want)
	}
	if got := Candidates(root, 1); len(got) != 1 {
		t.Errorf("limit not applied: %v", got)
	}
}

func TestInside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mk(t, root, map[string]os.FileMode{"bin/app": 0o755})
	mk(t, outside, map[string]os.FileMode{"evil": 0o755})
	os.Symlink(filepath.Join(outside, "evil"), filepath.Join(root, "escape"))
	os.Symlink("bin/app", filepath.Join(root, "alias"))

	if !Inside(filepath.Join(root, "bin/app"), root) {
		t.Error("regular file inside: want true")
	}
	if !Inside(filepath.Join(root, "alias"), root) {
		t.Error("symlink to a file inside: want true")
	}
	if Inside(filepath.Join(root, "escape"), root) {
		t.Error("symlink leaving the root: want false")
	}
	if Inside(filepath.Join(root, "../"+filepath.Base(outside)+"/evil"), root) {
		t.Error("../ path: want false")
	}
	if Inside(filepath.Join(root, "bin"), root) {
		t.Error("folder: want false")
	}
	if Inside(filepath.Join(root, "missing"), root) {
		t.Error("missing: want false")
	}
}
