package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveRegisteredApp(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)

	if err := Remove(h.env("n\n"), appID, false); exitCode(err) != 0 || !exists(h.inst()) {
		t.Fatalf("declining removed it: %v", err)
	}
	if err := Remove(h.env("y\n"), appID, false); err != nil {
		t.Fatalf("remove: %v\n%s", err, h.output())
	}
	for _, p := range []string{h.inst(), h.l.BackupDir(appID), filepath.Join(h.l.BinDir, "dselftest"), h.l.DesktopFile(appID), filepath.Join(h.l.RegistryDir, appID)} {
		if exists(p) {
			t.Errorf("%s still exists", p)
		}
	}
	if !strings.Contains(h.output(), "Dopt Selftest removed") {
		t.Errorf("output:\n%s", h.output())
	}
}

func TestRemoveRefusesUnregistered(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(h.inst(), 0o755)
	os.WriteFile(filepath.Join(h.inst(), "precious"), nil, 0o644)
	if err := Remove(h.env("y\n"), appID, true); exitCode(err) != 1 || !exists(filepath.Join(h.inst(), "precious")) {
		t.Errorf("removed an unregistered folder: %v", err)
	}
	if err := Remove(h.env(""), "not.installed", true); exitCode(err) != 1 {
		t.Errorf("missing app: %v", err)
	}
	if err := Remove(h.env(""), "../etc", true); exitCode(err) != 1 {
		t.Errorf("bad ID: %v", err)
	}
}

func TestRemoveKeepsForeignLauncherAndLinks(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	// The user replaced the launcher with their own, pointing elsewhere.
	os.WriteFile(h.l.DesktopFile(appID), []byte("[Desktop Entry]\nName=Mine\nExec=/usr/bin/other\n"), 0o644)
	other := filepath.Join(h.l.BinDir, "unrelated")
	os.Symlink("/usr/bin/true", other)

	if err := Remove(h.env(""), appID, true); err != nil {
		t.Fatal(err)
	}
	if !exists(h.l.DesktopFile(appID)) || !exists(other) {
		t.Error("removed a launcher or link dopt didn't create")
	}
}

func TestRemoveCleansUpMissingFolder(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	os.RemoveAll(h.inst())
	if err := Remove(h.env(""), appID, true); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(h.l.BinDir, "dselftest")) || exists(filepath.Join(h.l.RegistryDir, appID)) {
		t.Error("dangling link or registry entry left")
	}
}

func TestList(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	os.MkdirAll(filepath.Join(h.l.OptDir, "manual-app"), 0o755)
	apps := List(h.l)
	if len(apps) != 2 {
		t.Fatalf("List = %+v", apps)
	}
	a := apps[0]
	if a.ID != appID || !a.Registered || a.Command != "dselftest" || a.Name != "Dopt Selftest" || a.Size == 0 || a.Installed.IsZero() {
		t.Errorf("registered app = %+v", a)
	}
	if apps[1].ID != "manual-app" || apps[1].Registered {
		t.Errorf("other folder = %+v", apps[1])
	}
}
