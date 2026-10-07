package source

import (
	"archive/tar"
	"archive/zip"
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
	"time"

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
	if _, err := Download(context.Background(), srv.URL+"/app.tar.gz?token=x", dest, func(done, total int64) { calls++ }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, v1) {
		t.Error("downloaded content differs")
	}
	if calls == 0 {
		t.Error("progress never reported")
	}

	missing := filepath.Join(dir, "missing.tar.gz")
	_, err := Download(context.Background(), srv.URL+"/nope.tar.gz", missing, nil)
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
		"https://x.test/dl/app.zip":                       "app.zip",
		"https://x.test/dl/app.tar.xz":                    "app.tar.xz",
		"https://x.test/dl/App-1.0-x86_64.AppImage":       "App-1.0-x86_64.AppImage",
		"https://x.test/dl/app.exe":                       "myapp-linux.tar.gz",
		"https://x.test/dl/a%2F..%2Fb.tar.gz":             "myapp-linux.tar.gz",
		"https://x.test/dl/$(rm -rf).tar.gz":              "myapp-linux.tar.gz",
	}
	for in, want := range cases {
		if got := SaveName(in, "myapp", ".tar.gz"); got != want {
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
	if r, _ := Resolve(context.Background(), m, "arm64"); r.URL != "https://x/arm.tar.gz" {
		t.Errorf("arm64 = %s", r.URL)
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
		r, err := Resolve(context.Background(), m, arch)
		if err != nil || !strings.Contains(r.URL, want) {
			t.Errorf("%s: %s, %v", arch, r.URL, err)
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
		r, err := Resolve(context.Background(), manifest.Manifest{Source: &c.src}, c.arch)
		if err != nil || r.URL != c.want || r.Version != "v1.0" {
			t.Errorf("%+v %s: %+v, %v; want %s", c.src, c.arch, r, err, c.want)
		}
	}

	_, err := Resolve(context.Background(), manifest.Manifest{Source: &manifest.Source{Type: "github", Repository: "o/tool", AssetPattern: "windows"}}, "x64")
	if err == nil || !strings.Contains(err.Error(), "tool-linux-arm64.tar.gz") {
		t.Errorf("no match: err = %v, want the asset list", err)
	}
}

func TestResolveGitHubPrefersTarGz(t *testing.T) {
	release := []byte(`{"tag_name":"v2","assets":[
		{"name":"tool-linux-x86_64.zip","browser_download_url":"https://x/zip"},
		{"name":"Tool-x86_64-linux.AppImage","browser_download_url":"https://x/appimage"},
		{"name":"tool-linux-x86_64.tar.gz","browser_download_url":"https://x/tgz"}]}`)
	srv := serve(t, map[string][]byte{"/repos/o/tool/releases/latest": release})
	old := GitHubAPI
	GitHubAPI = srv.URL
	t.Cleanup(func() { GitHubAPI = old })
	r, err := Resolve(context.Background(), manifest.Manifest{Source: &manifest.Source{Type: "github", Repository: "o/tool"}}, "x64")
	if err != nil || r.URL != "https://x/tgz" {
		t.Errorf("automatic pick = %+v, %v", r, err)
	}
	r, err = Resolve(context.Background(), manifest.Manifest{Source: &manifest.Source{Type: "github", Repository: "o/tool", AssetPattern: "*.AppImage"}}, "x64")
	if err != nil || r.URL != "https://x/appimage" {
		t.Errorf("AppImage pattern = %+v, %v", r, err)
	}
}

func TestVersionFromURL(t *testing.T) {
	cases := map[string]string{
		"https://go.dev/dl/go1.27.1.linux-amd64.tar.gz":                                       "1.27.1",
		"https://github.com/neovim/neovim/releases/download/v0.12.5/nvim-linux-x86_64.tar.gz": "0.12.5",
		"https://dl.discordapp.net/apps/linux/0.0.111/discord-0.0.111.tar.gz":                 "0.0.111",
		"https://download.jetbrains.com/toolbox/jetbrains-toolbox-3.8.1.88030.tar.gz":         "3.8.1.88030",
		"https://discord.com/api/download?platform=linux&format=tar.gz":                       "",
		"https://192.168.1.10/app-linux-x86_64.tar.gz":                                        "",
	}
	for in, want := range cases {
		if got := VersionFromURL(in); got != want {
			t.Errorf("VersionFromURL(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestFingerprint(t *testing.T) {
	cases := []struct {
		f        Fetched
		want     string
		reliable bool
	}{
		{Fetched{URL: "https://gh/v1.2/a.tar.gz", FinalURL: "https://cdn/obj?sig=1"}, "url:https://gh/v1.2/a.tar.gz", true},
		{Fetched{URL: "https://x/download", FinalURL: "https://cdn/app-0.0.9.tar.gz?sig=abc"}, "url:https://cdn/app-0.0.9.tar.gz", true},
		{Fetched{URL: "https://x/latest.tar.gz", FinalURL: "https://x/latest.tar.gz", ETag: `"abc"`}, `etag:"abc"`, true},
		{Fetched{URL: "https://x/latest.tar.gz", FinalURL: "https://x/latest.tar.gz", LastModified: "Wed, 07 Oct 2026 10:00:00 GMT", Length: 42}, "modified:Wed, 07 Oct 2026 10:00:00 GMT:42", true},
		{Fetched{URL: "https://x/latest.tar.gz", FinalURL: "https://x/latest.tar.gz"}, "url:https://x/latest.tar.gz", false},
	}
	for _, c := range cases {
		fp, reliable := Fingerprint(c.f)
		if fp != c.want || reliable != c.reliable {
			t.Errorf("Fingerprint(%+v) = %s %v, want %s %v", c.f, fp, reliable, c.want, c.reliable)
		}
	}
	if v := Version(Resolved{Version: "v3"}, Fetched{URL: "https://x/a-1.0.tar.gz"}); v != "v3" {
		t.Errorf("Version prefers the source's: %s", v)
	}
	if v := Version(Resolved{}, Fetched{URL: "https://x/download", FinalURL: "https://cdn/a-2.1.tar.gz"}); v != "2.1" {
		t.Errorf("Version from final URL: %s", v)
	}
}

func TestProbe(t *testing.T) {
	v1 := tarGz(t, "v1")
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/files/app-1.4.2.tar.gz", http.StatusFound)
	})
	mux.HandleFunc("/files/app-1.4.2.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Write(v1)
	})
	// Like a signed CDN URL: HEAD is refused, GET works.
	mux.HandleFunc("/signed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "x", time.Time{}, bytes.NewReader(v1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	info, err := Probe(context.Background(), srv.URL+"/latest")
	if err != nil || !strings.HasSuffix(info.FinalURL, "/files/app-1.4.2.tar.gz") || info.ETag != `"v1"` {
		t.Errorf("Probe redirect = %+v, %v", info, err)
	}
	info, err = Probe(context.Background(), srv.URL+"/signed")
	if err != nil || info.Length != int64(len(v1)) {
		t.Errorf("Probe with HEAD refused = %+v, %v (want length %d)", info, err, len(v1))
	}
	if _, err := Probe(context.Background(), srv.URL+"/missing"); err == nil {
		t.Error("Probe 404: want error")
	}
}

func TestKeepZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("app/tool")
	w.Write([]byte("x"))
	zw.Close()
	src := filepath.Join(t.TempDir(), "dl")
	os.WriteFile(src, buf.Bytes(), 0o644)
	got, err := Keep(src, "https://x/download?id=1", "app", t.TempDir(), -1, -1)
	if err != nil || filepath.Base(got) != "app-linux.zip" {
		t.Errorf("Keep zip = %s, %v", got, err)
	}
}
