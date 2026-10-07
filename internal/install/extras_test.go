package install

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/registry"
	"github.com/Ominous-Josef/dopt/internal/source"
)

// toolchain builds selftest-app/bin/{dselftest,dfmt,dvet}.
func (h *harness) toolchain(version string, tools ...string) string {
	h.t.Helper()
	h.pkg("warmup", true)
	p := filepath.Join(h.root, "tc-"+version+".tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, tool := range append([]string{"dselftest"}, tools...) {
		tw.WriteHeader(&tar.Header{Name: "selftest-app/bin/" + tool, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(sleepBin))})
		tw.Write(sleepBin)
	}
	tw.WriteHeader(&tar.Header{Name: "selftest-app/VERSION", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(version))})
	tw.Write([]byte(version))
	tw.Close()
	gz.Close()
	f.Close()
	return p
}

func (h *harness) toolReq(archive string, extras ...manifest.Command) Request {
	req := h.req(archive)
	req.Manifest.CliOnly = true
	req.Manifest.Binaries = extras
	return req
}

func TestExtraCommands(t *testing.T) {
	h := newHarness(t)
	req := h.toolReq(h.toolchain("v1", "dfmt", "dvet"),
		manifest.Command{Path: "bin/dfmt"},
		manifest.Command{Pattern: "dve*", LinkAs: "selftest-vet"},
		manifest.Command{Path: "bin/missing"})
	must(t, h.run("y\n", req), h)

	if got := h.linkTarget("dfmt"); got != filepath.Join(h.inst(), "bin/dfmt") {
		t.Errorf("dfmt -> %s", got)
	}
	if got := h.linkTarget("selftest-vet"); got != filepath.Join(h.inst(), "bin/dvet") {
		t.Errorf("selftest-vet -> %s", got)
	}
	out := h.output()
	if !strings.Contains(out, "Commands   dselftest, dfmt, selftest-vet") || !strings.Contains(out, "'missing' (bin/missing) isn't in the archive") {
		t.Errorf("output:\n%s", out)
	}
	if e, _ := registry.Read(h.l.RegistryDir, appID); e[registry.KeyCommands] != "dselftest,dfmt,selftest-vet" {
		t.Errorf("registry commands = %q", e[registry.KeyCommands])
	}

	// The next version drops dvet: its link goes, the others stay.
	req = h.toolReq(h.toolchain("v2", "dfmt"), manifest.Command{Path: "bin/dfmt"})
	must(t, h.run("", req), h)
	if exists(filepath.Join(h.l.BinDir, "selftest-vet")) || h.linkTarget("dfmt") == "" {
		t.Errorf("links after update:\n%s", h.output())
	}

	// remove takes every link with it.
	if err := Remove(h.env(""), appID, true); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(h.l.BinDir, "dfmt")) {
		t.Error("extra link left after remove")
	}
}

func TestExtraCommandClash(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(h.l.BinDir, 0o755)
	os.WriteFile(filepath.Join(h.l.BinDir, "dfmt"), []byte("mine"), 0o755)
	req := h.toolReq(h.toolchain("v1", "dfmt"), manifest.Command{Path: "bin/dfmt"})

	req.Force = true
	if err := h.run("", req); exitCode(err) != 1 {
		t.Errorf("-i with a clash: %v", err)
	}
	req.Force = false
	if err := h.run("2\n", req); exitCode(err) != 1 {
		t.Errorf("abort: %v", err)
	}
	must(t, h.run("1\ny\n", req), h) // don't link dfmt
	if b, _ := os.ReadFile(filepath.Join(h.l.BinDir, "dfmt")); string(b) != "mine" {
		t.Error("foreign file replaced")
	}
	if !strings.Contains(h.output(), "Command    dselftest") {
		t.Errorf("skipped command still listed:\n%s", h.output())
	}

	// On PATH elsewhere: "link it anyway".
	h2 := newHarness(t)
	h2.paths["dfmt"] = "/usr/bin/dfmt"
	must(t, h2.run("2\ny\n", h2.toolReq(h2.toolchain("v1", "dfmt"), manifest.Command{Path: "bin/dfmt"})), h2)
	if h2.linkTarget("dfmt") == "" {
		t.Error("dfmt not linked after 'link it anyway'")
	}
}

func sha256Of(t *testing.T, p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestPublishedChecksums(t *testing.T) {
	h := newHarness(t)
	archive := h.pkg("v1", true)
	base := serve(t, map[string]string{"/app-1.0.tar.gz": archive})
	url := base + "/app-1.0.tar.gz"
	good := sha256Of(t, archive)

	run := func(input string, resolved source.Resolved) error {
		req := h.req("")
		resolved.URL = url
		req.Download, req.URL, req.Resolved = true, url, resolved
		return h.run(input, req)
	}

	if err := run("y\nn\n", source.Resolved{SHA256: good, ChecksumOrigin: "the release's published digest"}); err != nil {
		t.Fatalf("matching digest: %v\n%s", err, h.output())
	}
	if !strings.Contains(h.output(), "SHA-256 verified (the release's published digest)") {
		t.Errorf("output:\n%s", h.output())
	}

	err := run("", source.Resolved{SHA256: strings.Repeat("0", 64), ChecksumOrigin: "the release's published digest"})
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "doesn't match the release's published digest") {
		t.Errorf("mismatch: %v", err)
	}

	// An optional sums file that doesn't list the download: install anyway.
	sums := serve(t, map[string]string{"/SHA256SUMS": writeTemp(t, good+"  other.tar.gz\n")})
	if err := run("n\n", source.Resolved{ChecksumURL: sums + "/SHA256SUMS", ChecksumOrigin: "SHA256SUMS"}); err != nil {
		t.Errorf("optional, unlisted: %v\n%s", err, h.output())
	}
	// The same file, required by the recipe: refuse.
	err = run("", source.Resolved{ChecksumURL: sums + "/SHA256SUMS", ChecksumOrigin: "SHA256SUMS", ChecksumRequired: true})
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "doesn't list app-1.0.tar.gz") {
		t.Errorf("required, unlisted: %v", err)
	}
	// --sha256 always wins.
	req := h.req("")
	req.Download, req.URL, req.SHA256 = true, url, good
	req.Resolved = source.Resolved{URL: url, SHA256: strings.Repeat("0", 64)}
	if err := h.run("n\n", req); err != nil || !strings.Contains(h.output(), "SHA-256 verified (--sha256)") {
		t.Errorf("--sha256 precedence: %v\n%s", err, h.output())
	}
}

func writeTemp(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte(content), 0o644)
	return p
}
