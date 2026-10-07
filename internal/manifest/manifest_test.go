package manifest

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAllRecipesParse(t *testing.T) {
	files, err := filepath.Glob("../../recipes/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no recipes found: %v", err)
	}
	for _, f := range files {
		m, _, err := Load(f)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if m.Name == "" || m.SymlinkAs == "" || m.Categories == "" {
			t.Errorf("%s: defaults not applied: %+v", filepath.Base(f), m)
		}
	}
}

// Copy of dopt-bash/examples/example-manifest.json: cli_only is a string there.
const bashExample = `{
  "app_id": "com.example.app",
  "name": "Example App",
  "comment": "An example application managed by dopt",
  "binary_pattern": "example-bin",
  "binary_path": "bin/example-bin",
  "icon_path": "icon.png",
  "cli_only": "false",
  "symlink_as": "example",
  "categories": "Utility;Development;",
  "exec_flags": "--mode=gui",
  "default_url_x64": "https://example.com/downloads/example-app-x86_64-linux.tar.gz",
  "default_url_arm64": "https://example.com/downloads/example-app-aarch64-linux.tar.gz"
}`

func TestBashManifestParses(t *testing.T) {
	m, warnings, err := Parse([]byte(bashExample))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if m.CliOnly || m.IconPath != "icon.png" || m.DefaultURL("arm64") == "" {
		t.Errorf("parsed = %+v", m)
	}
}

func TestFlexBool(t *testing.T) {
	cases := map[string]bool{`true`: true, `"true"`: true, `false`: false, `"false"`: false, `null`: false, `""`: false}
	for in, want := range cases {
		m, _, err := Parse([]byte(`{"app_id":"a","binary_path":"a","cli_only":` + in + `}`))
		if err != nil {
			t.Errorf("cli_only %s: %v", in, err)
			continue
		}
		if bool(m.CliOnly) != want {
			t.Errorf("cli_only %s = %v, want %v", in, m.CliOnly, want)
		}
	}
	if _, _, err := Parse([]byte(`{"app_id":"a","binary_path":"a","cli_only":"maybe"}`)); err == nil {
		t.Error(`cli_only "maybe": want error`)
	}
}

func TestDefaults(t *testing.T) {
	m, _, err := Parse([]byte(`{"app_id":"tool","binary_pattern":"tool","name":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "tool" || m.SymlinkAs != "tool" || m.Categories != "Utility;" {
		t.Errorf("defaults = %+v", m)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		`{}`:                                  "app_id",
		`{"app_id":"../x","binary_path":"a"}`: "app_id",
		`{"app_id":"a"}`:                      "binary_path",
		`{"app_id":"a","binary_path":"a","symlink_as":"a/b"}`:         "symlink_as",
		`{"app_id":"a","binary_path":"a","schema":3}`:                 "schema 3",
		`{"app_id":"a","binary_path":"a","source":{"type":"ftp"}}`:    "source type",
		`{"app_id":"a","binary_path":"a","source":{"type":"github"}}`: "repository",
		`not json`: "valid JSON",
	}
	for in, want := range cases {
		_, _, err := Parse([]byte(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%s) error = %v, want mention of %q", in, err, want)
		}
	}
}

func TestWarnings(t *testing.T) {
	_, warnings, err := Parse([]byte(`{"app_id":"a","binary_path":"a","default_install_dir":"/opt/x","colour":"red","source":{"type":"github","repository":"o/r","asset":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"default_install_dir", "'colour'", "'source.asset'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q missing %s", joined, want)
		}
	}
}

func TestArch(t *testing.T) {
	if a, _ := Arch("amd64"); a != "x64" {
		t.Errorf("amd64 -> %s", a)
	}
	if a, _ := Arch("arm64"); a != "arm64" {
		t.Errorf("arm64 -> %s", a)
	}
	if _, err := Arch("riscv64"); err == nil {
		t.Error("riscv64: want error")
	}
}

func TestBinaries(t *testing.T) {
	m, warnings, err := Parse([]byte(`{"app_id":"golang","binary_path":"bin/go","symlink_as":"go",
		"binaries":[{"path":"bin/gofmt"},{"pattern":"go-*","link_as":"gotool"}]}`))
	if err != nil || len(warnings) != 0 {
		t.Fatal(err, warnings)
	}
	if m.Binaries[0].Name() != "gofmt" || m.Binaries[1].Name() != "gotool" {
		t.Errorf("names = %s, %s", m.Binaries[0].Name(), m.Binaries[1].Name())
	}
	bad := map[string]string{
		`[{"path":"bin/go"}]`:                     "used twice",
		`[{"path":"../x"}]`:                       "inside the app folder",
		`[{"pattern":"go*"}]`:                     "link_as",
		`[{}]`:                                    "exactly one",
		`[{"path":"a","pattern":"b"}]`:            "exactly one",
		`[{"path":"bin/x","link_as":"bad/name"}]`: "command name",
	}
	for bins, want := range bad {
		_, _, err := Parse([]byte(`{"app_id":"golang","binary_path":"bin/go","symlink_as":"go","binaries":` + bins + `}`))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", bins, err, want)
		}
	}
}

func TestGitLabSource(t *testing.T) {
	if _, _, err := Parse([]byte(`{"app_id":"glab","binary_path":"bin/glab","source":{"type":"gitlab","repository":"gitlab-org/cli","checksums":"checksums.txt"}}`)); err != nil {
		t.Error(err)
	}
	if _, _, err := Parse([]byte(`{"app_id":"glab","binary_path":"bin/glab","source":{"type":"gitlab"}}`)); err == nil {
		t.Error("gitlab without repository: want error")
	}
}
