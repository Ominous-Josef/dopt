//go:build e2e

// End-to-end tests: build the dopt binary and run it against a throwaway root
// (DOPT_TEST_ROOT), answering prompts on stdin. They follow the sections of
// dopt-bash's tests/run.sh. Run with: go test -tags e2e ./e2e/
package e2e

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const appID = "com.dopt.selftest"

var doptBin string

func TestMain(m *testing.M) {
	if os.Geteuid() == 0 {
		fmt.Println("e2e tests run in local mode and must not run as root")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "dopt-e2e-bin-")
	if err != nil {
		panic(err)
	}
	doptBin = filepath.Join(dir, "dopt")
	build := exec.Command("go", "build", "-o", doptBin, "..")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type sandbox struct {
	t    *testing.T
	dir  string
	root string // DOPT_TEST_ROOT
	cwd  string
	path string // PATH for dopt
	out  string
	code int
}

func newSandbox(t *testing.T) *sandbox {
	dir := t.TempDir()
	s := &sandbox{t: t, dir: dir, root: filepath.Join(dir, "root"), cwd: filepath.Join(dir, "cwd")}
	os.MkdirAll(s.cwd, 0o755)
	os.MkdirAll(filepath.Join(s.root, "Downloads"), 0o755)
	s.path = filepath.Join(s.root, "bin") + ":/usr/bin:/bin"
	return s
}

func (s *sandbox) opt(rel ...string) string {
	return filepath.Join(append([]string{s.root, "opt"}, rel...)...)
}

// run executes dopt with answers on stdin.
func (s *sandbox) run(answers string, args ...string) {
	s.t.Helper()
	cmd := exec.Command(doptBin, args...)
	cmd.Dir = s.cwd
	cmd.Stdin = strings.NewReader(answers)
	cmd.Env = []string{"DOPT_TEST_ROOT=" + s.root, "HOME=" + os.Getenv("HOME"), "PATH=" + s.path, "LANG=C"}
	out, err := cmd.CombinedOutput()
	s.out = string(out)
	s.code = 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		s.code = exitErr.ExitCode()
	} else if err != nil {
		s.t.Fatal(err)
	}
}

func (s *sandbox) check(desc string, ok bool) {
	s.t.Helper()
	if !ok {
		s.t.Errorf("%s\n--- dopt output (exit %d) ---\n%s", desc, s.code, s.out)
	}
}

func (s *sandbox) has(text string) bool { return strings.Contains(s.out, text) }

func (s *sandbox) version() string {
	b, _ := os.ReadFile(s.opt(appID, "VERSION"))
	return string(b)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

var sleepBin []byte

// pkg writes an archive in one of dopt-bash's fixture layouts.
func (s *sandbox) pkg(name, version, layout string) string {
	s.t.Helper()
	if sleepBin == nil {
		b, err := os.ReadFile("/usr/bin/sleep")
		if err != nil {
			s.t.Skip("no /usr/bin/sleep")
		}
		sleepBin = b
	}
	p := filepath.Join(s.dir, name)
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	add := func(n string, mode int64, body []byte) {
		tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(body))})
		tw.Write(body)
	}
	switch layout {
	case "wrap":
		add("selftest-app/bin/dselftest", 0o755, sleepBin)
		add("selftest-app/VERSION", 0o644, []byte(version))
		add("selftest-app/icon.png", 0o644, []byte("PNG"))
	case "loose":
		add("bin/dselftest", 0o755, sleepBin)
		add("lib/x.txt", 0o644, []byte("x"))
		add("VERSION", 0o644, []byte(version))
	case "files":
		add("dselftest", 0o755, sleepBin)
		add("VERSION", 0o644, []byte(version))
	case "deep":
		add("selftest-app/opt/bin/dselftest", 0o755, sleepBin)
		add("selftest-app/VERSION", 0o644, []byte(version))
	}
	tw.Close()
	gz.Close()
	f.Close()
	return p
}

func (s *sandbox) manifest(name, extra string) string {
	p := filepath.Join(s.dir, name)
	body := `{"app_id":"` + appID + `","name":"Dopt Selftest","binary_path":"bin/dselftest","icon_path":"icon.png","symlink_as":"dselftest","cli_only":"false"` + extra + `}`
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func TestInstallAndUpdate(t *testing.T) {
	s := newSandbox(t)
	m := s.manifest("m.json", "")
	s.run("y\nn\n", "install", "-m", m, "-f", s.pkg("v1.tar.gz", "v1", "wrap"))
	s.check("fresh install", s.code == 0 && s.version() == "v1")
	s.check("registered", exists(s.opt(".dopt", appID)))
	s.check("plain output when piped", !strings.Contains(s.out, "\x1b["))

	s.run("n\n", "-m", m, "-f", s.pkg("v2.tar.gz", "v2", "wrap")) // dopt 2.x flag-only form
	s.check("update via the flag-only form", s.code == 0 && s.version() == "v2" && s.has("updated"))

	s.run("n\n", "install", "-m", m, "-f", s.pkg("v3.tar.gz", "v3", "wrap"), "-i")
	s.check("forced update", s.code == 0 && s.version() == "v3")
}

func TestOwnershipPrompt(t *testing.T) {
	s := newSandbox(t)
	os.MkdirAll(s.opt(appID), 0o755)
	m := s.manifest("m.json", "")
	s.run("", "install", "-i", "-m", m, "-f", s.pkg("v1.tar.gz", "v1", "wrap"))
	s.check("-i refuses an unregistered folder", s.code == 1 && s.has("forced (-i) mode"))
	s.run("n\n", "install", "-m", m, "-f", s.pkg("v1.tar.gz", "v1", "wrap"))
	s.check("declining exits 0 and changes nothing", s.code == 0 && s.version() == "")
}

func TestArchiveLayouts(t *testing.T) {
	for _, layout := range []string{"loose", "files", "deep"} {
		t.Run(layout, func(t *testing.T) {
			s := newSandbox(t)
			m := s.manifest("m.json", `,"binary_path":"","binary_pattern":"dselftest"`)
			s.run("y\nn\n", "install", "-m", m, "-f", s.pkg("a.tar.gz", layout, layout))
			s.check(layout+" archive installs", s.code == 0 && s.version() == layout)
			target, _ := os.Readlink(filepath.Join(s.root, "bin", "dselftest"))
			s.check("link points at the binary", strings.HasSuffix(target, "dselftest") && strings.HasPrefix(target, s.opt(appID)))
		})
	}
}

func TestDownloadsAndResume(t *testing.T) {
	s := newSandbox(t)
	v1 := s.pkg("app.tar.gz", "v1", "wrap")
	srv := httptest.NewServer(http.FileServer(http.Dir(s.dir)))
	defer srv.Close()
	m := s.manifest("m.json", "")

	s.run("y\nn\n", "install", "-m", m, "-u", srv.URL+"/app.tar.gz?token=abc")
	s.check("download installs", s.code == 0 && s.version() == "v1")
	s.check("saved without the query string", exists(filepath.Join(s.cwd, "app.tar.gz")))
	s.check("token not shown", !s.has("token=abc"))

	s.run("", "install", "-m", m, "-u", srv.URL+"/missing.tar.gz", "-i")
	s.check("404 fails", s.code == 1 && s.has("Download failed"))
	_ = v1
}

func TestRunningAppDecline(t *testing.T) {
	s := newSandbox(t)
	m := s.manifest("m.json", "")
	s.run("y\nn\n", "install", "-m", m, "-f", s.pkg("v1.tar.gz", "v1", "wrap"))
	app := exec.Command(s.opt(appID, "bin", "dselftest"), "30")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	go app.Wait()
	defer app.Process.Kill()
	time.Sleep(50 * time.Millisecond)

	v2 := s.pkg("v2.tar.gz", "v2", "wrap")
	s.run("n\n", "install", "-m", m, "-f", v2)
	s.check("keeping the app running exits 0", s.code == 0 && s.version() == "v1")
	s.check("resume command shown", s.has(doptBin+" install -m "+m+" -f "+v2))
}

func TestSourcePicker(t *testing.T) {
	s := newSandbox(t)
	dl := filepath.Join(s.root, "Downloads")
	old := s.pkg("old.tar.gz", "old", "wrap")
	newer := s.pkg("newer.tar.gz", "newer", "wrap")
	os.Rename(old, filepath.Join(dl, "old.tar.gz"))
	os.Rename(newer, filepath.Join(dl, "newer.tar.gz"))
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(filepath.Join(dl, "old.tar.gz"), past, past)
	m := s.manifest("m.json", "")

	s.run("", "install", "-i", "-m", m)
	s.check("-i without a source stops", s.code == 1 && s.has("No archive given"))

	s.run("1\ny\nn\n", "install", "-m", m)
	s.check("newest download is option 1", s.has("1) newer.tar.gz") && s.has("2) old.tar.gz  (2 days ago)"))
	s.check("picking it installs", s.code == 0 && s.version() == "newer")

	s.run("p\n'"+filepath.Join(dl, "old.tar.gz")+"'\nn\n", "install", "-m", m)
	s.check("typed (quoted) path installs", s.code == 0 && s.version() == "old")

	s.run("a\n", "install", "-m", m)
	s.check("abort stops", s.code == 1 && s.has("Deployment aborted"))
}

func TestWizard(t *testing.T) {
	s := newSandbox(t)
	a := s.pkg("a.tar.gz", "v1", "wrap")
	// App ID, name, command, CLI-only, binary, icon, category, install?, launch?
	s.run("?\n"+appID+"\nMy Tool\nmytool\nn\nbin/dselftest\n\nDevelopment\ny\nn\n", "install", "-f", a)
	s.check("wizard install", s.code == 0 && s.version() == "v1")
	s.check("'?' lists apps", s.has("none registered yet"))
	desk, _ := os.ReadFile(filepath.Join(s.root, "applications", appID+".desktop"))
	s.check("category gets its ';'", strings.Contains(string(desk), "Categories=Development;"))

	// Update: every answer defaults to the previous install's.
	s.run("\n\n\n\n\n\nn\n", "install", "-a", appID, "-f", s.pkg("b.tar.gz", "v2", "wrap"))
	s.check("defaults come from the existing install", s.code == 0 && s.version() == "v2" &&
		s.has("[My Tool]") && s.has("[mytool]") && s.has("[bin/dselftest]") && s.has("[Development;]"))
}

func TestExistingGlobalInstall(t *testing.T) {
	s := newSandbox(t)
	os.MkdirAll(filepath.Join(s.root, "global-opt", appID), 0o755)
	os.MkdirAll(filepath.Join(s.root, "global-applications"), 0o755)
	os.WriteFile(filepath.Join(s.root, "global-applications", appID+".desktop"), []byte("[Desktop Entry]\nName=Selftest\n"), 0o644)
	m := s.manifest("m.json", "")
	a := s.pkg("a.tar.gz", "v1", "wrap")

	s.run("2\ny\ny\nn\n", "install", "-m", m, "-f", a)
	s.check("separate local copy", s.code == 0 && exists(s.opt(appID+"-local", "VERSION")) && s.has("Dopt Selftest (Local) installed"))

	s.run("3\n", "install", "-m", m, "-f", a)
	s.check("abort", s.code == 1)

	// Option 1 re-runs with sudo: a fake sudo on PATH records its arguments.
	fakeBin := filepath.Join(s.dir, "fakebin")
	os.MkdirAll(fakeBin, 0o755)
	record := filepath.Join(s.dir, "sudo-args")
	os.WriteFile(filepath.Join(fakeBin, "sudo"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+record+"\n"), 0o755)
	s.path = fakeBin + ":" + s.path
	s.run("1\n", "install", "-m", m, "-f", a, "-c")
	args, _ := os.ReadFile(record)
	s.check("elevating keeps all original options",
		string(args) == doptBin+"\ninstall\n-g\n-m\n"+m+"\n-f\n"+a+"\n-c\n")
}

func TestNothingInRealHome(t *testing.T) {
	if exists(filepath.Join(os.Getenv("HOME"), ".local", "opt", appID)) {
		t.Error("an e2e test installed into the real ~/.local/opt")
	}
}

func TestListAndRemove(t *testing.T) {
	s := newSandbox(t)
	s.run("", "list")
	s.check("empty list", s.code == 0 && s.has("Nothing installed by dopt"))

	s.run("y\nn\n", "install", "-m", s.manifest("m.json", ""), "-f", s.pkg("v1.tar.gz", "v1", "wrap"))
	os.MkdirAll(s.opt("manual-app"), 0o755)
	s.run("", "list")
	s.check("list shows the app and other folders", s.code == 0 && s.has(appID) && s.has("dselftest") && s.has("manual-app"))

	s.run("", "remove", "manual-app", "-i")
	s.check("won't remove an unregistered folder", s.code == 1 && exists(s.opt("manual-app")))

	s.run("y\n", "remove", appID)
	s.check("remove", s.code == 0 && !exists(s.opt(appID)) && !exists(filepath.Join(s.root, "bin", "dselftest")) &&
		!exists(filepath.Join(s.root, "applications", appID+".desktop")))
}
