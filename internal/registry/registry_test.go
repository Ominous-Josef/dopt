package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFolderIdentityMatchesStat(t *testing.T) {
	dir := t.TempDir()
	id, err := FolderIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("stat", "-c", "%i:%W", "--", dir).Output()
	if err != nil {
		t.Skip("GNU stat not available")
	}
	if want := strings.TrimSpace(string(out)); id != want {
		t.Errorf("FolderIdentity = %s, stat says %s", id, want)
	}
}

func TestCheckLifecycle(t *testing.T) {
	opt := t.TempDir()
	reg := filepath.Join(opt, ".dopt")
	inst := filepath.Join(opt, "app")

	if s, _ := Check(reg, "app", inst); s != Absent {
		t.Errorf("no folder: %v", s)
	}
	os.Mkdir(inst, 0o755)
	if s, _ := Check(reg, "app", inst); s != Unregistered {
		t.Errorf("folder without entry: %v", s)
	}
	id, _ := FolderIdentity(inst)
	if err := Write(reg, "app", Entry{KeyAppID: "app", KeyFolderID: id, KeyInstalled: "now", KeyCommand: "app"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := Check(reg, "app", inst); s != Registered {
		t.Errorf("registered folder: %v", s)
	}
	// Renaming keeps the identity.
	os.Rename(inst, inst+".x")
	os.Rename(inst+".x", inst)
	if s, _ := Check(reg, "app", inst); s != Registered {
		t.Errorf("after rename: %v", s)
	}
	// Deleting and recreating does not. Hold the old inode so it can't be reused.
	hold := filepath.Join(opt, "hold")
	os.Rename(inst, hold)
	os.Mkdir(inst, 0o755)
	if s, _ := Check(reg, "app", inst); s != Recreated {
		t.Errorf("recreated folder: %v", s)
	}
}

// dopt-bash writes app_id, folder_id and installed with printf; both tools must read each other's entries.
func TestBashFormatInterop(t *testing.T) {
	reg := t.TempDir()
	bashEntry := "app_id=app\nfolder_id=123:456\ninstalled=2026-10-07T20:16:39+01:00\n"
	os.WriteFile(filepath.Join(reg, "app"), []byte(bashEntry), 0o644)
	e, err := Read(reg, "app")
	if err != nil || e[KeyFolderID] != "123:456" {
		t.Fatalf("Read bash entry = %v, %v", e, err)
	}

	if err := Write(reg, "app", Entry{KeyAppID: "app", KeyFolderID: "1:2", KeyInstalled: "t", KeySource: "https://x/a.tar.gz", KeyBinary: "bin/a"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(reg, "app"))
	if !strings.HasPrefix(string(data), "app_id=app\nfolder_id=1:2\ninstalled=t\n") {
		t.Errorf("entry = %q, want dopt-bash's leading keys", data)
	}
	// What dopt-bash's registry_get does: sed -n "s/^folder_id=//p" | head -n 1
	out, err := exec.Command("sed", "-n", "s/^folder_id=//p", filepath.Join(reg, "app")).Output()
	if err == nil && strings.TrimSpace(string(out)) != "1:2" {
		t.Errorf("bash-style lookup = %q", out)
	}
}

func TestWriteRejectsBadInput(t *testing.T) {
	reg := t.TempDir()
	if err := Write(reg, "../x", Entry{}); err == nil {
		t.Error("bad app ID: want error")
	}
	if err := Write(reg, "app", Entry{KeySource: "a\nfolder_id=evil"}); err == nil {
		t.Error("newline in value: want error")
	}
}

func TestListAndRemove(t *testing.T) {
	reg := t.TempDir()
	Write(reg, "b", Entry{KeyAppID: "b"})
	Write(reg, "a", Entry{KeyAppID: "a"})
	os.WriteFile(filepath.Join(reg, ".a.tmp-1"), nil, 0o644)
	ids, _ := List(reg)
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("List = %v", ids)
	}
	if err := Remove(reg, "a"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(reg, "a"); err != nil {
		t.Errorf("removing a missing entry: %v", err)
	}
	if ids, _ := List(filepath.Join(reg, "missing")); ids != nil {
		t.Errorf("missing dir: %v", ids)
	}
}
