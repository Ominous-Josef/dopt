package install

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ominous-Josef/dopt/internal/registry"
	"github.com/ulikunitz/xz"
)

// writePkg writes the wrapped selftest app in another format: "tar.xz" or "zip".
func (h *harness) writePkg(format, version string) string {
	h.t.Helper()
	h.pkg("warmup", true) // loads sleepBin
	files := []struct {
		name string
		mode int64
		body []byte
	}{
		{"selftest-app/bin/dselftest", 0o755, sleepBin},
		{"selftest-app/VERSION", 0o644, []byte(version)},
	}
	var buf bytes.Buffer
	switch format {
	case "tar.xz":
		xw, _ := xz.NewWriter(&buf)
		tw := tar.NewWriter(xw)
		for _, f := range files {
			tw.WriteHeader(&tar.Header{Name: f.name, Typeflag: tar.TypeReg, Mode: f.mode, Size: int64(len(f.body))})
			tw.Write(f.body)
		}
		tw.Close()
		xw.Close()
	case "zip":
		zw := zip.NewWriter(&buf)
		for _, f := range files {
			hdr := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
			hdr.SetMode(os.FileMode(f.mode))
			w, _ := zw.CreateHeader(hdr)
			w.Write(f.body)
		}
		zw.Close()
	}
	p := filepath.Join(h.root, "pkg-"+version+"."+format)
	os.WriteFile(p, buf.Bytes(), 0o644)
	return p
}

func TestOtherArchiveFormats(t *testing.T) {
	for _, format := range []string{"tar.xz", "zip"} {
		t.Run(format, func(t *testing.T) {
			h := newHarness(t)
			must(t, h.run("y\nn\n", h.req(h.writePkg(format, "v1"))), h)
			if h.version() != "v1" || h.linkTarget("dselftest") != filepath.Join(h.inst(), "bin/dselftest") {
				t.Errorf("version=%q link=%s\n%s", h.version(), h.linkTarget("dselftest"), h.output())
			}
			if !strings.Contains(h.output(), "Format: "+format) {
				t.Errorf("format not shown:\n%s", h.output())
			}
			if info, _ := os.Stat(filepath.Join(h.inst(), "bin/dselftest")); info == nil || info.Mode().Perm()&0o111 == 0 {
				t.Error("binary not executable")
			}
			if e, _ := registry.Read(h.l.RegistryDir, appID); e[registry.KeyFormat] != format {
				t.Errorf("registry format = %q", e[registry.KeyFormat])
			}
		})
	}
}

func TestBareBinaryDownload(t *testing.T) {
	h := newHarness(t)
	h.pkg("warmup", true)
	bin := filepath.Join(h.root, "dselftest-linux-amd64")
	os.WriteFile(bin, sleepBin, 0o644)

	req := h.req(bin)
	req.Manifest.BinaryPath, req.Manifest.IconPath = "", ""
	req.Manifest.BinaryPattern = "dselftest"
	req.Manifest.CliOnly = true
	must(t, h.run("y\n", req), h)
	if got := h.linkTarget("dselftest"); got != filepath.Join(h.inst(), "dselftest") {
		t.Errorf("link = %s\n%s", got, h.output())
	}
	if !strings.Contains(h.output(), "Format: binary") {
		t.Errorf("output:\n%s", h.output())
	}
	// An update replaces the single file like any other install.
	must(t, h.run("", req), h)
	if entries, _ := os.ReadDir(h.inst()); len(entries) != 1 {
		t.Errorf("install folder = %v", entries)
	}
}

func TestDownloadRecordsReleaseAndRecipe(t *testing.T) {
	h := newHarness(t)
	base := serve(t, map[string]string{"/app-2.4.1.tar.gz": h.pkg("v1", true)})
	req := h.req("")
	req.Download, req.URL = true, base+"/app-2.4.1.tar.gz"
	must(t, h.run("y\nn\n", req), h)

	e, _ := registry.Read(h.l.RegistryDir, appID)
	if e[registry.KeyVersion] != "2.4.1" || e[registry.KeyRelease] != "url:"+base+"/app-2.4.1.tar.gz" {
		t.Errorf("registry = %v", e)
	}
	if !strings.Contains(h.output(), "Version    2.4.1") {
		t.Errorf("summary misses the version:\n%s", h.output())
	}
	m, ok, err := registry.ReadRecipe(h.l.RegistryDir, appID)
	if !ok || err != nil || m.SymlinkAs != "dselftest" || m.BinaryPath != "bin/dselftest" {
		t.Fatalf("recipe = %+v, %v, %v", m, ok, err)
	}
	// The -u URL becomes the recipe's fixed URL, so `dopt update` can check it.
	if m.DefaultURLX64 != base+"/app-2.4.1.tar.gz" && m.DefaultURLArm64 != base+"/app-2.4.1.tar.gz" {
		t.Errorf("recipe has no URL: %+v", m)
	}

	if err := Remove(h.env(""), appID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := registry.ReadRecipe(h.l.RegistryDir, appID); ok {
		t.Error("recipe kept after remove")
	}
}

func TestBatchSkipsLaunchPrompt(t *testing.T) {
	h := newHarness(t)
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	req := h.req(h.pkg("v2", true))
	req.Batch = true
	must(t, h.run("", req), h)
	if strings.Contains(h.output(), "Launch Dopt Selftest now?") || len(h.launched) != 0 {
		t.Errorf("batch update prompted or launched %v:\n%s", h.launched, h.output())
	}
}
