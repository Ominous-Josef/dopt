package owner

import (
	"os"
	"path/filepath"
	"testing"
)

func fake(t *testing.T, outputs map[string]string) {
	t.Helper()
	old := Runner
	Runner = func(name string, args ...string) ([]byte, bool) {
		out, ok := outputs[name]
		return []byte(out), ok
	}
	t.Cleanup(func() { Runner = old })
}

func TestRPMOwned(t *testing.T) {
	fake(t, map[string]string{"rpm": "file /x is not owned by any package\ngolang\n"})
	if o, ok := Find(t.TempDir()); !ok || o != (Owner{"golang", "dnf"}) {
		t.Errorf("Find = %+v, %v", o, ok)
	}
}

func TestDpkgOwned(t *testing.T) {
	fake(t, map[string]string{
		"rpm":  "file /x is not owned by any package\n",
		"dpkg": "diversion by foo from: /x\nlibfoo:amd64, libbar: /opt/x/lib\n",
	})
	if o, ok := Find(t.TempDir()); !ok || o != (Owner{"libfoo", "apt"}) {
		t.Errorf("Find = %+v, %v", o, ok)
	}
}

func TestNotOwned(t *testing.T) {
	fake(t, map[string]string{"rpm": "file /x is not owned by any package\n"})
	if _, ok := Find(t.TempDir()); ok {
		t.Error("unowned folder reported as owned")
	}
	fake(t, map[string]string{})
	if _, ok := Find(t.TempDir()); ok {
		t.Error("no package manager: reported as owned")
	}
}

func TestSampleLimits(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		os.WriteFile(filepath.Join(dir, string(rune('a'+i%26))+string(rune('0'+i/26))), nil, 0o644)
	}
	deep := filepath.Join(dir, "1", "2", "3", "4")
	os.MkdirAll(deep, 0o755)
	if got := len(sample(dir)); got != 21 {
		t.Errorf("sample size = %d, want dir + 20 files", got)
	}
	only := t.TempDir()
	os.MkdirAll(filepath.Join(only, "a", "b", "c", "d"), 0o755)
	os.WriteFile(filepath.Join(only, "a", "b", "c", "d", "deep"), nil, 0o644)
	os.WriteFile(filepath.Join(only, "a", "b", "shallow"), nil, 0o644)
	got := sample(only)
	if len(got) != 2 || got[1] != filepath.Join(only, "a", "b", "shallow") {
		t.Errorf("sample = %v, want only files up to depth 3", got)
	}
}

// The real rpm (if installed) must not claim a fresh temp folder.
func TestRealToolsOnTempDir(t *testing.T) {
	if _, ok := Find(t.TempDir()); ok {
		t.Error("a new temp folder is reported as package-owned")
	}
}
