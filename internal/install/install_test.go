package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/owner"
	"github.com/Ominous-Josef/dopt/internal/proc"
	"github.com/Ominous-Josef/dopt/internal/registry"
	"github.com/Ominous-Josef/dopt/internal/sysuser"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

const appID = "com.dopt.selftest"

type harness struct {
	t        *testing.T
	root     string
	l        layout.Layout
	cwd      string
	out, err bytes.Buffer
	owned    bool
	paths    map[string]string // fake PATH lookups
	launched []string
	pathEnv  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("install tests run in local mode, which refuses root")
	}
	root := t.TempDir()
	l, err := layout.Resolve(false, filepath.Join(root, "home"), os.Geteuid(), root)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, root: root, l: l, cwd: filepath.Join(root, "cwd"), paths: map[string]string{}}
	h.pathEnv = l.BinDir
	os.MkdirAll(h.cwd, 0o755)
	return h
}

func (h *harness) env(input string) Env {
	h.out.Reset()
	h.err.Reset()
	u := ui.New(strings.NewReader(input), &h.out, &h.err, false, false)
	usr, _ := sysuser.Real()
	return Env{
		Layout:  h.l,
		User:    usr,
		UI:      u,
		Program: "dopt",
		Cwd:     h.cwd,
		PathEnv: h.pathEnv,
		FindOwner: func(string) (owner.Owner, bool) {
			return owner.Owner{Package: "selftest-pkg", Tool: "dnf"}, h.owned
		},
		Lookup: func(name string) string { return h.paths[name] },
		Launch: func(bin string) error { h.launched = append(h.launched, bin); return nil },
		Now:    func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) },
	}
}

func (h *harness) run(input string, req Request) error {
	return Run(context.Background(), h.env(input), req)
}

func (h *harness) output() string { return h.out.String() + h.err.String() }

func (h *harness) inst() string { return h.l.InstallDir(appID) }

func (h *harness) version() string {
	b, _ := os.ReadFile(filepath.Join(h.inst(), "VERSION"))
	return string(b)
}

func (h *harness) linkTarget(name string) string {
	t, _ := os.Readlink(filepath.Join(h.l.BinDir, name))
	return t
}

func baseManifest() manifest.Manifest {
	m := manifest.Manifest{AppID: appID, Name: "Dopt Selftest", BinaryPath: "bin/dselftest", IconPath: "icon.png", SymlinkAs: "dselftest"}
	m.ApplyDefaults()
	return m
}

func (h *harness) req(archive string) Request {
	return Request{Manifest: baseManifest(), ManifestPath: "m.json", Archive: archive}
}

var sleepBin []byte

// pkg builds a wrapped archive: selftest-app/{bin/dselftest, VERSION, icon.png}.
func (h *harness) pkg(version string, withBinary bool) string {
	h.t.Helper()
	if sleepBin == nil {
		b, err := os.ReadFile("/usr/bin/sleep")
		if err != nil {
			h.t.Skip("no /usr/bin/sleep")
		}
		sleepBin = b
	}
	p := filepath.Join(h.root, "pkg-"+version+".tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, body []byte) {
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(body))})
		tw.Write(body)
	}
	if withBinary {
		add("selftest-app/bin/dselftest", 0o755, sleepBin)
	} else {
		add("selftest-app/bin/other-tool", 0o755, []byte("#!/bin/sh\n"))
	}
	add("selftest-app/VERSION", 0o644, []byte(version))
	add("selftest-app/icon.png", 0o644, []byte("PNG"))
	tw.Close()
	gz.Close()
	f.Close()
	return p
}

func exitCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return -1
	}
	return 0
}

func must(t *testing.T, err error, h *harness) {
	t.Helper()
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, h.output())
	}
}

// ---- Install and update ----

func TestFreshInstallAndUpdate(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)

	if h.version() != "v1" {
		t.Errorf("version = %q", h.version())
	}
	if got, want := h.linkTarget("dselftest"), filepath.Join(h.inst(), "bin/dselftest"); got != want {
		t.Errorf("link = %s, want %s", got, want)
	}
	e, _ := registry.Read(h.l.RegistryDir, appID)
	id, _ := registry.FolderIdentity(h.inst())
	if e[registry.KeyFolderID] != id || e[registry.KeyCommand] != "dselftest" || e[registry.KeyBinary] != "bin/dselftest" {
		t.Errorf("registry = %v", e)
	}
	desk, _ := os.ReadFile(h.l.DesktopFile(appID))
	for _, want := range []string{"Name=Dopt Selftest", `Exec="` + filepath.Join(h.l.BinDir, "dselftest") + `"`, "Icon=" + filepath.Join(h.inst(), "icon.png")} {
		if !strings.Contains(string(desk), want) {
			t.Errorf("desktop file missing %q:\n%s", want, desk)
		}
	}
	out := h.output()
	for _, want := range []string{"[1/4] Checking Dopt Selftest...", "[4/4] Creating menu shortcut...", "[+] Dopt Selftest installed", "Command    dselftest"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Same App ID = update: no install or replace prompt, only the launch prompt.
	must(t, h.run("n\n", h.req(h.pkg("v2", true))), h)
	if h.version() != "v2" {
		t.Errorf("after update version = %q", h.version())
	}
	if !strings.Contains(h.output(), "Dopt Selftest updated") || strings.Contains(h.output(), "Replace it") {
		t.Errorf("update output:\n%s", h.output())
	}
	if entries, _ := os.ReadDir(h.l.OptDir); len(entries) != 2 { // app + .dopt
		t.Errorf("leftovers in opt dir: %v", entries)
	}
}

func TestDeclineFreshInstall(t *testing.T) {
	h := newHarness(t)
	err := h.run("n\n", h.req(h.pkg("v1", true)))
	if exitCode(err) != 0 || err == nil || exists(h.inst()) {
		t.Errorf("err = %v, installed = %v", err, exists(h.inst()))
	}
}

func TestLaunchPrompt(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\ny\n", h.req(h.pkg("v1", true))), h)
	if len(h.launched) != 1 {
		t.Errorf("launched = %v", h.launched)
	}
}

// ---- Ownership ----

func TestUnregisteredFolder(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(h.inst(), 0o755)
	os.WriteFile(filepath.Join(h.inst(), "precious"), []byte("x"), 0o644)

	req := h.req(h.pkg("v1", true))
	err := h.run("n\n", req)
	if exitCode(err) != 0 || !exists(filepath.Join(h.inst(), "precious")) {
		t.Errorf("declined replace: err=%v, folder changed", err)
	}
	if !strings.Contains(h.output(), "isn't registered") || !strings.Contains(h.output(), "precious") {
		t.Errorf("output:\n%s", h.output())
	}

	req.Force = true
	if err := h.run("", req); exitCode(err) != 1 || !exists(filepath.Join(h.inst(), "precious")) {
		t.Errorf("-i must refuse: %v", err)
	}

	req.Force = false
	must(t, h.run("y\nn\n", req), h)
	if exists(filepath.Join(h.inst(), "precious")) || h.version() != "v1" {
		t.Error("confirmed replace didn't replace")
	}
}

func TestRecreatedFolderAsksAgain(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	os.Rename(h.inst(), filepath.Join(h.root, "hold")) // keep the inode busy
	os.MkdirAll(h.inst(), 0o755)
	err := h.run("n\n", h.req(h.pkg("v2", true)))
	if exitCode(err) != 0 || !strings.Contains(h.output(), "replaced or recreated") {
		t.Errorf("err=%v\n%s", err, h.output())
	}
}

func TestBashRegistryEntryCounts(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	// Rewrite the entry exactly as dopt-bash would.
	out, err := exec.Command("stat", "-c", "%i:%W", h.inst()).Output()
	if err != nil {
		t.Skip("no GNU stat")
	}
	entry := "app_id=" + appID + "\nfolder_id=" + strings.TrimSpace(string(out)) + "\ninstalled=2026-10-07T20:16:39+01:00\n"
	os.WriteFile(filepath.Join(h.l.RegistryDir, appID), []byte(entry), 0o644)
	must(t, h.run("n\n", h.req(h.pkg("v2", true))), h)
	if strings.Contains(h.output(), "Replace it") {
		t.Error("a dopt-bash registry entry wasn't recognized")
	}
}

func TestPackageOwnedFolder(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	h.owned = true
	err := h.run("", h.req(h.pkg("v2", true)))
	if exitCode(err) != 1 || h.version() != "v1" {
		t.Errorf("err=%v version=%s", err, h.version())
	}
	var e *Error
	errors.As(err, &e)
	if !strings.Contains(e.Msg, "selftest-pkg") || !strings.Contains(strings.Join(e.Hints, " "), "sudo dnf remove selftest-pkg") {
		t.Errorf("error = %+v", e)
	}
}

// ---- Recovery ----

func TestMissingBinaryLeavesInstallUntouched(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	err := h.run("", h.req(h.pkg("broken", false)))
	if exitCode(err) != 1 || h.version() != "v1" || exists(h.l.StageDir(appID)) {
		t.Errorf("err=%v version=%s stage left=%v", err, h.version(), exists(h.l.StageDir(appID)))
	}
	if !strings.Contains(h.output(), "bin/other-tool") {
		t.Errorf("candidates not listed:\n%s", h.output())
	}
}

func TestInterruptedSwapIsRecovered(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	// A previous run died between "live -> backup" and "staged -> live".
	os.Rename(h.inst(), h.l.BackupDir(appID))
	os.MkdirAll(filepath.Join(h.l.StageDir(appID), "junk"), 0o755)

	must(t, h.run("n\n", h.req(h.pkg("v2", true))), h)
	if !strings.Contains(h.output(), "left aside by an interrupted update") || strings.Contains(h.output(), "Install Dopt Selftest") {
		t.Errorf("recovery output:\n%s", h.output())
	}
	if h.version() != "v2" || exists(h.l.BackupDir(appID)) || exists(h.l.StageDir(appID)) {
		t.Errorf("version=%s backup=%v stage=%v", h.version(), exists(h.l.BackupDir(appID)), exists(h.l.StageDir(appID)))
	}
}

func TestCleanupRestoresBackup(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	env := h.env("")
	m := baseManifest()
	r := &run{env: &env, req: &Request{}, m: &m, u: env.UI, l: h.l, swapping: true,
		inst: h.inst(), stage: h.l.StageDir(appID), backup: h.l.BackupDir(appID)}
	os.Rename(h.inst(), r.backup)
	os.MkdirAll(r.stage, 0o755)
	r.cleanup(errors.New("boom"))
	if h.version() != "v1" || exists(r.stage) {
		t.Errorf("not restored: version=%s stage=%v", h.version(), exists(r.stage))
	}
	if !strings.Contains(h.output(), "previous version was restored") {
		t.Errorf("output:\n%s", h.output())
	}
}

func TestAssertManaged(t *testing.T) {
	h := newHarness(t)
	m := baseManifest()
	r := &run{m: &m, l: h.l}
	for _, ok := range []string{h.inst(), h.l.StageDir(appID), h.l.BackupDir(appID)} {
		if err := r.assertManaged(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{h.l.OptDir, filepath.Join(h.l.OptDir, "other"), filepath.Join(h.inst(), "sub"), "/", filepath.Join(h.l.OptDir, "..", appID)} {
		if err := r.assertManaged(bad); err == nil {
			t.Errorf("%s: want refusal", bad)
		}
	}
}

// ---- Command names ----

func TestForeignFileInBinIsNeverReplaced(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(h.l.BinDir, 0o755)
	foreign := filepath.Join(h.l.BinDir, "dselftest")
	os.WriteFile(foreign, []byte("mine"), 0o755)

	req := h.req(h.pkg("v1", true))
	req.Force = true
	if err := h.run("", req); exitCode(err) != 1 || !strings.Contains(h.output(), "forced (-i) mode") {
		t.Errorf("-i: err=%v\n%s", err, h.output())
	}

	req.Force = false
	must(t, h.run("1\nbad/name\nselftest2\ny\nn\n", req), h)
	if b, _ := os.ReadFile(foreign); string(b) != "mine" {
		t.Error("foreign file was modified")
	}
	if h.linkTarget("selftest2") == "" {
		t.Error("renamed link not created")
	}
	out := h.output()
	if !strings.Contains(out, "Invalid name 'bad/name'") || !strings.Contains(out, "Tip: the manifest's 'symlink_as'") {
		t.Errorf("output:\n%s", out)
	}
}

func TestNameOnPath(t *testing.T) {
	h := newHarness(t)
	h.paths["dselftest"] = "/usr/bin/dselftest"
	req := h.req(h.pkg("v1", true))
	req.Force = true
	if err := h.run("", req); exitCode(err) != 1 {
		t.Errorf("-i with a PATH clash: %v", err)
	}
	req.Force = false
	must(t, h.run("2\ny\nn\n", req), h)
	if !strings.Contains(h.output(), "will coexist") || h.linkTarget("dselftest") == "" {
		t.Errorf("continue anyway:\n%s", h.output())
	}

	// Another install of the same app only warns.
	h2 := newHarness(t)
	h2.paths["dselftest"] = filepath.Join(h2.l.GlobalOptDir, appID, "bin/dselftest")
	must(t, h2.run("y\nn\n", h2.req(h2.pkg("v1", true))), h2)
	if !strings.Contains(h2.output(), "another install of this app") {
		t.Errorf("same-app warning missing:\n%s", h2.output())
	}
}

func TestRenamedCommandRemovesOldLink(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	req := h.req(h.pkg("v2", true))
	req.Manifest.SymlinkAs = "newname"
	req.SymlinkFromCLI = true
	must(t, h.run("n\n", req), h)
	if exists(filepath.Join(h.l.BinDir, "dselftest")) || h.linkTarget("newname") == "" {
		t.Errorf("old link kept or new link missing:\n%s", h.output())
	}
}

// ---- Desktop ----

func TestCLIOnlyRemovesShortcut(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	req := h.req(h.pkg("v2", true))
	req.Manifest.CliOnly = true
	must(t, h.run("", req), h)
	if exists(h.l.DesktopFile(appID)) {
		t.Error("desktop file kept for a CLI-only app")
	}
	out := h.output()
	if !strings.Contains(out, "[3/3] Installing...") || !strings.Contains(out, "Shortcut   none (CLI-only)") || !strings.Contains(out, "Removed the old menu shortcut") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDesktopEscaping(t *testing.T) {
	h := newHarness(t)
	req := h.req(h.pkg("v1", true))
	req.Manifest.Name = "Evil\nExec=rm -rf ~"
	must(t, h.run("y\nn\n", req), h)
	desk, _ := os.ReadFile(h.l.DesktopFile(appID))
	if strings.Count(string(desk), "\nExec=") != 1 {
		t.Errorf("injected Exec line:\n%s", desk)
	}
}

// ---- Processes ----

func (h *harness) startApp() *exec.Cmd {
	h.t.Helper()
	cmd := exec.Command(filepath.Join(h.inst(), "bin/dselftest"), "30")
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	go cmd.Wait()
	h.t.Cleanup(func() { cmd.Process.Kill() })
	time.Sleep(50 * time.Millisecond)
	return cmd
}

func TestRunningAppIsStoppedAndRelaunched(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	cmd := h.startApp()

	req := h.req(h.pkg("v2", true))
	req.Force = true
	must(t, h.run("", req), h)
	if proc.Alive(cmd.Process.Pid) {
		t.Error("app still running after a forced update")
	}
	if h.version() != "v2" || len(h.launched) != 1 || !strings.Contains(h.output(), "Relaunched") {
		t.Errorf("version=%s launched=%v\n%s", h.version(), h.launched, h.output())
	}
}

func TestKeepingTheAppRunningCancels(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	cmd := h.startApp()
	archive := h.pkg("v2", true)
	err := h.run("n\n", h.req(archive))
	if exitCode(err) != 0 || err == nil {
		t.Errorf("err = %v", err)
	}
	if !proc.Alive(cmd.Process.Pid) || h.version() != "v1" || exists(h.l.StageDir(appID)) {
		t.Error("declining must leave the app running and the install untouched")
	}
	if !strings.Contains(h.output(), "dopt install -m m.json -f "+archive) {
		t.Errorf("resume hint missing:\n%s", h.output())
	}
}

// ---- Downloads and archives ----

func serve(t *testing.T, files map[string]string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, p)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestDownloads(t *testing.T) {
	h := newHarness(t)
	base := serve(t, map[string]string{"/app.tar.gz": h.pkg("v1", true)})

	req := h.req("")
	req.Download, req.URL = true, base+"/app.tar.gz?token=abc"
	must(t, h.run("y\nn\n", req), h)
	if h.version() != "v1" || !exists(filepath.Join(h.cwd, "app.tar.gz")) {
		t.Errorf("download install: version=%s\n%s", h.version(), h.output())
	}
	if strings.Contains(h.output(), "token=abc") {
		t.Error("query string shown in output")
	}

	req.Cleanup = true
	os.Remove(filepath.Join(h.cwd, "app.tar.gz"))
	must(t, h.run("n\n", req), h)
	if exists(filepath.Join(h.cwd, "app.tar.gz")) || !strings.Contains(h.output(), "not kept (-c)") {
		t.Errorf("-c kept the download:\n%s", h.output())
	}

	req.Cleanup = false
	req.URL = base + "/missing.tar.gz"
	if err := h.run("", req); exitCode(err) != 1 || !strings.Contains(err.Error(), "Download failed") {
		t.Errorf("404: %v\n%s", err, h.output())
	}
	if entries, _ := os.ReadDir(h.cwd); len(entries) != 0 {
		t.Errorf("failed download left files: %v", entries)
	}

	req.URL = base + "/app.tar.gz"
	req.SHA256 = strings.Repeat("0", 64)
	if err := h.run("", req); exitCode(err) != 1 || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Errorf("sha mismatch: %v\n%s", err, h.output())
	}
	if entries, _ := os.ReadDir(h.cwd); len(entries) != 0 {
		t.Errorf("mismatched download was kept: %v", entries)
	}
}

func TestFailedUpdateKeepsDownloadWithResumeHint(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	base := serve(t, map[string]string{"/broken.tar.gz": h.pkg("broken", false)})
	req := h.req("")
	req.Download, req.URL = true, base+"/broken.tar.gz"
	if err := h.run("", req); exitCode(err) != 1 {
		t.Fatalf("err = %v", err)
	}
	if !exists(filepath.Join(h.cwd, "broken.tar.gz")) || !strings.Contains(h.output(), "-f "+filepath.Join(h.cwd, "broken.tar.gz")) {
		t.Errorf("download not kept or no resume hint:\n%s", h.output())
	}
}

func TestCleanupLocalArchive(t *testing.T) {
	for _, c := range []struct {
		input   string
		force   bool
		deleted bool
	}{{"y\ny\nn\n", false, true}, {"y\nn\nn\n", false, false}, {"", true, true}} {
		h := newHarness(t)
		archive := h.pkg("v1", true)
		req := h.req(archive)
		req.Cleanup, req.Force = true, c.force
		must(t, h.run(c.input, req), h)
		if exists(archive) == c.deleted {
			t.Errorf("input %q force %v: archive exists = %v\n%s", c.input, c.force, exists(archive), h.output())
		}
	}
}

func TestWizardPicksBinary(t *testing.T) {
	h := newHarness(t)
	req := h.req(h.pkg("v1", false))
	req.Wizard, req.ManifestPath = true, ""
	req.Manifest.BinaryPath = "missing"
	must(t, h.run("y\n1\nn\n", req), h)
	if got := h.linkTarget("dselftest"); got != filepath.Join(h.inst(), "bin/other-tool") {
		t.Errorf("link = %s\n%s", got, h.output())
	}
}

func TestBinaryPathCannotEscape(t *testing.T) {
	h := newHarness(t)
	req := h.req(h.pkg("v1", true))
	req.Manifest.BinaryPath = "../../../../usr/bin/sleep"
	if err := h.run("y\n", req); exitCode(err) != 1 || exists(h.inst()) {
		t.Errorf("escaping binary_path accepted: %v", err)
	}
}

func TestPathNote(t *testing.T) {
	h := newHarness(t)
	h.pathEnv = "/usr/bin"
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	if !strings.Contains(h.output(), "isn't on your PATH") {
		t.Errorf("missing PATH note:\n%s", h.output())
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[int64]string{0: "0B", 4096: "4.0K", 12 << 20: "12M", 1536 << 20: "1.5G"} {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{"a/b.tar.gz": "a/b.tar.gz", "My App": "'My App'", "it's": `'it'\''s'`} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
