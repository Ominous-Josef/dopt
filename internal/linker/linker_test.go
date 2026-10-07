package linker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectAndLink(t *testing.T) {
	root := t.TempDir()
	inst := filepath.Join(root, "opt", "app")
	other := filepath.Join(root, "opt", "app2")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{inst, other, bin} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(inst, "app"), nil, 0o755)
	link := filepath.Join(bin, "app")

	if s := Inspect(link, inst); s != Free {
		t.Errorf("empty: %v", s)
	}
	if err := Link(filepath.Join(inst, "app"), link); err != nil {
		t.Fatal(err)
	}
	if s := Inspect(link, inst); s != Ours {
		t.Errorf("our link: %v", s)
	}
	if s := Inspect(link, other); s != Foreign {
		t.Errorf("link into another app: %v", s)
	}
	// "app2" shares a prefix with "app" but is a different folder.
	os.WriteFile(filepath.Join(other, "x"), nil, 0o755)
	Link(filepath.Join(other, "x"), link)
	if s := Inspect(link, inst); s != Foreign {
		t.Errorf("prefix-sharing folder: %v", s)
	}

	// Relative links resolve against the bin folder.
	os.Remove(link)
	os.Symlink("../opt/app/app", link)
	if s := Inspect(link, inst); s != Ours {
		t.Errorf("relative link: %v", s)
	}

	plain := filepath.Join(bin, "plain")
	os.WriteFile(plain, nil, 0o755)
	if s := Inspect(plain, inst); s != Foreign {
		t.Errorf("regular file: %v", s)
	}
	if removed, _ := RemoveIfOurs(plain, inst); removed {
		t.Error("removed a file that isn't ours")
	}
	if removed, err := RemoveIfOurs(link, inst); !removed || err != nil {
		t.Errorf("RemoveIfOurs = %v, %v", removed, err)
	}
}

func TestDanglingLinkIntoInstallIsOurs(t *testing.T) {
	root := t.TempDir()
	inst := filepath.Join(root, "app")
	link := filepath.Join(root, "cmd")
	os.Symlink(filepath.Join(inst, "gone"), link)
	if s := Inspect(link, inst); s != Ours {
		t.Errorf("dangling link into the (missing) install: %v", s)
	}
}

func TestOnPath(t *testing.T) {
	if !OnPath("/home/a/.local/bin", "/usr/bin:/home/a/.local/bin/") {
		t.Error("want on PATH")
	}
	if OnPath("/home/a/.local/bin", "/usr/bin::/bin") {
		t.Error("want not on PATH")
	}
}
