package layout

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveModes(t *testing.T) {
	home := "/home/alice"

	local, err := Resolve(false, home, 1000, "")
	if err != nil {
		t.Fatal(err)
	}
	if local.OptDir != "/home/alice/.local/opt" || local.BinDir != "/home/alice/.local/bin" ||
		local.DesktopDir != "/home/alice/.local/share/applications" || local.RegistryDir != "/home/alice/.local/opt/.dopt" {
		t.Errorf("local layout = %+v", local)
	}
	if local.GlobalOptDir != "/opt" {
		t.Errorf("local GlobalOptDir = %q, want /opt", local.GlobalOptDir)
	}

	global, err := Resolve(true, home, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !global.Global || global.OptDir != "/opt" || global.BinDir != "/usr/local/bin" || global.RegistryDir != "/opt/.dopt" {
		t.Errorf("global layout = %+v", global)
	}

	if _, err := Resolve(true, home, 1000, ""); err == nil {
		t.Error("global without root: want error")
	}
	if _, err := Resolve(false, home, 0, ""); err == nil {
		t.Error("local as root: want error")
	}
	if _, err := Resolve(true, home, 0, "/tmp/x"); err == nil {
		t.Error("global in test mode: want error")
	}
}

func TestResolveTestRoot(t *testing.T) {
	root := t.TempDir()
	l, err := Resolve(false, "/home/alice", 1000, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{l.OptDir, l.BinDir, l.DesktopDir, l.RegistryDir, l.GlobalOptDir, l.GlobalDesktopDir, l.DownloadsDir} {
		if rel, err := filepath.Rel(root, p); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("%s is outside the test root %s", p, root)
		}
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
}

func TestDerivedPaths(t *testing.T) {
	l, _ := Resolve(false, "/home/a", 1000, "")
	if got := l.StageDir("app"); got != "/home/a/.local/opt/.app.dopt-new" {
		t.Errorf("StageDir = %s", got)
	}
	if got := l.BackupDir("app"); got != "/home/a/.local/opt/.app.dopt-old" {
		t.Errorf("BackupDir = %s", got)
	}
	if got := l.DesktopFile("app"); got != "/home/a/.local/share/applications/app.desktop" {
		t.Errorf("DesktopFile = %s", got)
	}
}
