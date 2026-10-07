package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ominous-Josef/dopt/internal/manifest"
)

func tarGz(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "app/VERSION", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func serve(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownload(t *testing.T) {
	v1 := tarGz(t, "v1")
	srv := serve(t, map[string][]byte{"/app.tar.gz": v1})
	dir := t.TempDir()

	dest := filepath.Join(dir, "dl.tar.gz")
	var calls int
	if err := Download(context.Background(), srv.URL+"/app.tar.gz?token=x", dest, func(done, total int64) { calls++ }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, v1) {
		t.Error("downloaded content differs")
	}
	if calls == 0 {
		t.Error("progress never reported")
	}

	missing := filepath.Join(dir, "missing.tar.gz")
	err := Download(context.Background(), srv.URL+"/nope.tar.gz", missing, nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: err = %v", err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("a failed download left a file behind")
	}
}

func TestRedactHidesQuery(t *testing.T) {
	if got := redact("https://x.test/a.tar.gz?token=secret"); strings.Contains(got, "secret") {
		t.Errorf("redact = %s", got)
	}
}

func TestVerifySHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte("hello"), 0o644)
	const helloSum = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if _, err := VerifySHA256(p, helloSum); err != nil {
		t.Errorf("match: %v", err)
	}
	actual, err := VerifySHA256(p, strings.Repeat("0", 64))
	if !errors.Is(err, ErrChecksum) || actual != helloSum {
		t.Errorf("mismatch: %s, %v", actual, err)
	}
}

func TestSaveName(t *testing.T) {
	cases := map[string]string{
		"https://x.test/dl/app-1.2.tar.gz":                "app-1.2.tar.gz",
		"https://x.test/dl/app.tar.gz?token=abc#frag":     "app.tar.gz",
		"https://x.test/dl/My%20App.tgz":                  "My App.tgz",
		"https://discord.com/api/download?platform=linux": "myapp-linux.tar.gz",
		"https://x.test/dl/.hidden.tar.gz":                "myapp-linux.tar.gz",
		"https://x.test/dl/app.zip":                       "myapp-linux.tar.gz",
		"https://x.test/dl/a%2F..%2Fb.tar.gz":             "myapp-linux.tar.gz",
		"https://x.test/dl/$(rm -rf).tar.gz":              "myapp-linux.tar.gz",
	}
	for in, want := range cases {
		if got := SaveName(in, "myapp"); got != want {
			t.Errorf("SaveName(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestKeepNeverOverwrites(t *testing.T) {
	cwd := t.TempDir()
	work := t.TempDir()
	v1, v2 := tarGz(t, "v1"), tarGz(t, "v2")
	put := func(data []byte) string {
		p := filepath.Join(work, "dl.tar.gz")
		os.WriteFile(p, data, 0o644)
		return p
	}
	const u = "https://x.test/app.tar.gz?x=1"

	first, err := Keep(put(v1), u, "app", cwd, -1, -1)
	if err != nil || first != filepath.Join(cwd, "app.tar.gz") {
		t.Fatalf("first keep = %s, %v", first, err)
	}
	same, err := Keep(put(v1), u, "app", cwd, -1, -1)
	if err != nil || same != first {
		t.Errorf("identical keep = %s, %v; want reuse of %s", same, err, first)
	}
	if _, err := os.Stat(filepath.Join(cwd, "app-1.tar.gz")); err == nil {
		t.Error("identical download was saved again as -1")
	}
	other, err := Keep(put(v2), u, "app", cwd, -1, -1)
	if err != nil || other != filepath.Join(cwd, "app-1.tar.gz") {
		t.Errorf("different keep = %s, %v", other, err)
	}
	if got, _ := os.ReadFile(first); !bytes.Equal(got, v1) {
		t.Error("original copy was modified")
	}

	if _, err := Keep(put([]byte("garbage")), u, "app", cwd, -1, -1); err == nil {
		t.Error("kept an unreadable archive")
	}
}

func TestResolveDefaultURL(t *testing.T) {
	m := manifest.Manifest{AppID: "a", DefaultURLX64: "https://x/x64.tar.gz", DefaultURLArm64: "https://x/arm.tar.gz"}
	if u, _ := Resolve(context.Background(), m, "arm64"); u != "https://x/arm.tar.gz" {
		t.Errorf("arm64 = %s", u)
	}
	m.DefaultURLArm64 = ""
	if _, err := Resolve(context.Background(), m, "arm64"); err == nil {
		t.Error("missing arm64 URL: want error")
	}
}

func TestResolveAPIWithArch(t *testing.T) {
	srv := serve(t, map[string][]byte{"/dl": []byte(`[{"version":"go1.99","files":[
		{"os":"linux","arch":"amd64","filename":"go1.99.linux-amd64.tar.gz"},
		{"os":"linux","arch":"arm64","filename":"go1.99.linux-arm64.tar.gz"}]}]`)})
	m := manifest.Manifest{Source: &manifest.Source{
		Type:     "api",
		Endpoint: srv.URL + "/dl",
		JqQuery:  `.[0].files[] | select(.os == $os and .arch == $goarch) | "https://go.dev/dl/\(.filename)"`,
	}}
	for arch, want := range map[string]string{"x64": "linux-amd64", "arm64": "linux-arm64"} {
		u, err := Resolve(context.Background(), m, arch)
		if err != nil || !strings.Contains(u, want) {
			t.Errorf("%s: %s, %v", arch, u, err)
		}
	}
}

func TestResolveGitHub(t *testing.T) {
	release := []byte(`{"tag_name":"v1.0","assets":[
		{"name":"tool-linux-x86_64.tar.gz.sha256sum","browser_download_url":"https://x/sum"},
		{"name":"tool-linux-x86_64.tar.gz","browser_download_url":"https://x/x64"},
		{"name":"tool-linux-arm64.tar.gz","browser_download_url":"https://x/arm"},
		{"name":"tool-macos-arm64.tar.gz","browser_download_url":"https://x/mac"}]}`)
	srv := serve(t, map[string][]byte{"/repos/o/tool/releases/latest": release})
	old := GitHubAPI
	GitHubAPI = srv.URL
	t.Cleanup(func() { GitHubAPI = old })

	cases := []struct {
		src  manifest.Source
		arch string
		want string
	}{
		{manifest.Source{AssetPatternX64: "linux-x86_64", AssetPatternArm64: "linux-arm64"}, "x64", "https://x/x64"},
		{manifest.Source{AssetPatternX64: "linux-x86_64", AssetPatternArm64: "linux-arm64"}, "arm64", "https://x/arm"},
		{manifest.Source{AssetPattern: "tool-linux-*64.tar.gz"}, "x64", "https://x/x64"},
		{manifest.Source{}, "x64", "https://x/x64"},
		{manifest.Source{}, "arm64", "https://x/arm"},
	}
	for _, c := range cases {
		c.src.Type, c.src.Repository = "github", "o/tool"
		u, err := Resolve(context.Background(), manifest.Manifest{Source: &c.src}, c.arch)
		if err != nil || u != c.want {
			t.Errorf("%+v %s: %s, %v; want %s", c.src, c.arch, u, err, c.want)
		}
	}

	_, err := Resolve(context.Background(), manifest.Manifest{Source: &manifest.Source{Type: "github", Repository: "o/tool", AssetPattern: "windows"}}, "x64")
	if err == nil || !strings.Contains(err.Error(), "tool-linux-arm64.tar.gz") {
		t.Errorf("no match: err = %v, want the asset list", err)
	}
}
