package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	cases := map[string]string{
		"My App":                  "My App",
		"Evil\nExec=rm -rf ~":     "Evil Exec=rm -rf ~",
		"tabs\t\tand  \r\nbreaks": "tabs and breaks",
		`back\slash`:              `back\\slash`,
	}
	for in, want := range cases {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExecPath(t *testing.T) {
	if got, _ := ExecPath("/home/a/.local/bin/my 100%app"); got != `"/home/a/.local/bin/my 100%%app"` {
		t.Errorf("ExecPath = %s", got)
	}
	for _, bad := range []string{`/a/"q`, "/a/`x`", "/a/$HOME", `/a\b`, "/a\nb"} {
		if _, err := ExecPath(bad); err == nil {
			t.Errorf("ExecPath(%q): want error", bad)
		}
	}
}

func TestRender(t *testing.T) {
	out, err := Render(Entry{
		Name:    "App\nExec=evil",
		Command: "/home/a/.local/bin/app",
		Flags:   "--no-sandbox %U",
		WMClass: "app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\nExec=") != 1 || strings.Count(out, "Name=") != 1 {
		t.Errorf("injected keys:\n%s", out)
	}
	for _, want := range []string{`Exec="/home/a/.local/bin/app" --no-sandbox %U`, "Icon=system-run", "Categories=Utility;", "StartupWMClass=app"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Comment=") {
		t.Error("empty Comment= written")
	}
}

func TestWriteAndValidate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.desktop")
	if err := Write(file, Entry{Name: "App", Comment: "An app", Command: "/usr/local/bin/app", Categories: "Development;", WMClass: "app"}); err != nil {
		t.Fatal(err)
	}
	if msg := Validate(file); msg != "" {
		t.Errorf("desktop-file-validate: %s", msg)
	}
	if removed, err := Remove(file); !removed || err != nil {
		t.Errorf("Remove = %v, %v", removed, err)
	}
	if removed, err := Remove(file); removed || err != nil {
		t.Errorf("Remove missing = %v, %v", removed, err)
	}
}

func TestFindIcon(t *testing.T) {
	mk := func(root string, rels ...string) {
		for _, r := range rels {
			p := filepath.Join(root, r)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte("PNG"), 0o644)
		}
	}
	root := t.TempDir()
	mk(root, "node_modules/x/logo.png", ".cache/a.png", "resources/app/deep/other.png", "share/icons/tool.svg", "res/Brand.PNG")

	if got := FindIcon(root, "res/Brand.PNG", "app", "tool"); got != filepath.Join(root, "res/Brand.PNG") {
		t.Errorf("hint path: %s", got)
	}
	if got := FindIcon(root, "brand.png", "app", "tool"); got != filepath.Join(root, "res/Brand.PNG") {
		t.Errorf("hint name (case-insensitive search): %s", got)
	}
	if got := FindIcon(root, "", "app", "tool"); got != filepath.Join(root, "share/icons/tool.svg") {
		t.Errorf("command-named icon: %s", got)
	}

	fallback := t.TempDir()
	mk(fallback, "node_modules/x/logo.png", ".cache/a.png", "tests/t.png", "locales/l.png", "resources/app/deep/other.png")
	if got := FindIcon(fallback, "", "app", "tool"); got != filepath.Join(fallback, "resources/app/deep/other.png") {
		t.Errorf("fallback skips noisy folders: %s", got)
	}
	if got := FindIcon(t.TempDir(), "", "app", "tool"); got != "" {
		t.Errorf("no icons: %s", got)
	}
	if got := FindIcon(root, "../../etc/passwd", "app", "tool"); strings.Contains(got, "etc/passwd") {
		t.Errorf("hint escaped the install folder: %s", got)
	}
}
