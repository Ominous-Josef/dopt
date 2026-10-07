package source

import (
	"context"
	"strings"
	"testing"

	"github.com/Ominous-Josef/dopt/internal/manifest"
)

const (
	sumA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sumB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestParseSums(t *testing.T) {
	cases := []struct{ text, file, want string }{
		{sumA + "  tool-linux.tar.gz\n" + sumB + "  tool-mac.tar.gz\n", "tool-linux.tar.gz", sumA},
		{sumB + " *tool-mac.tar.gz\n" + sumA + " *./dist/tool-linux.tar.gz\n", "tool-linux.tar.gz", sumA},
		{"SHA256 (tool-linux.tar.gz) = " + strings.ToUpper(sumA) + "\n", "tool-linux.tar.gz", sumA},
		{sumA + "\n", "anything.tar.gz", sumA},                                  // a lone hash (sidecar file)
		{sumA + "  other.tar.gz\n", "tool-linux.tar.gz", ""},                    // not listed
		{"# comment\nnot a hash  tool-linux.tar.gz\n", "tool-linux.tar.gz", ""}, // junk
		{sumA + "  my tool.tar.gz\n", "my tool.tar.gz", sumA},                   // spaces in names
	}
	for _, c := range cases {
		if got := parseSums(c.text, c.file); got != c.want {
			t.Errorf("parseSums(%q, %s) = %q, want %q", c.text, c.file, got, c.want)
		}
	}
}

func rel(assets ...asset) release { return release{Repo: "o/tool", Tag: "v1", Assets: assets} }

func TestPickAssetChecksums(t *testing.T) {
	tool := asset{Name: "tool-linux-x86_64.tar.gz", URL: "https://x/tool"}
	digested := tool
	digested.Digest = "sha256:" + sumA
	src := func(checksums string) *manifest.Source {
		return &manifest.Source{Type: "github", Repository: "o/tool", AssetPattern: "tool-linux-x86_64", Checksums: checksums}
	}
	cases := []struct {
		name     string
		release  release
		src      *manifest.Source
		wantSHA  string
		wantURL  string
		required bool
	}{
		{"digest", rel(digested, asset{Name: "SHA256SUMS", URL: "https://x/sums"}), src(""), sumA, "", false},
		{"sidecar", rel(tool, asset{Name: tool.Name + ".sha256", URL: "https://x/side"}), src(""), "", "https://x/side", false},
		{"sums file", rel(tool, asset{Name: "sha256sum.txt", URL: "https://x/sums"}), src(""), "", "https://x/sums", false},
		{"goreleaser style", rel(tool, asset{Name: "tool_1.0_checksums.txt", URL: "https://x/gr"}), src(""), "", "https://x/gr", false},
		{"explicit beats digest", rel(digested, asset{Name: "MY-SUMS", URL: "https://x/mine"}), src("MY-SUMS"), "", "https://x/mine", true},
		{"none", rel(digested), src("none"), "", "", false},
		{"nothing published", rel(tool), src(""), "", "", false},
	}
	for _, c := range cases {
		r, err := pickAsset(c.release, c.src, "x64")
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if r.URL != "https://x/tool" || r.SHA256 != c.wantSHA || r.ChecksumURL != c.wantURL || r.ChecksumRequired != c.required {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	if _, err := pickAsset(rel(tool), src("SHA512SUMS"), "x64"); err == nil {
		t.Error("missing explicit checksum file: want error")
	}
}

func TestChecksumFetch(t *testing.T) {
	srv := serve(t, map[string][]byte{
		"/sums": []byte(sumB + "  other.tar.gz\n" + sumA + "  tool-linux-x86_64.tar.gz\n"),
	})
	r := Resolved{URL: "https://x/dl/tool-linux-x86_64.tar.gz", ChecksumURL: srv.URL + "/sums", ChecksumOrigin: "sums"}
	if got, err := Checksum(context.Background(), r); err != nil || got != sumA {
		t.Errorf("Checksum = %q, %v", got, err)
	}
	r.URL = "https://x/dl/unlisted.tar.gz"
	if got, err := Checksum(context.Background(), r); err != nil || got != "" {
		t.Errorf("optional, unlisted = %q, %v", got, err)
	}
	r.ChecksumRequired = true
	if _, err := Checksum(context.Background(), r); err == nil || !strings.Contains(err.Error(), "doesn't list unlisted.tar.gz") {
		t.Errorf("required, unlisted: %v", err)
	}
	if got, _ := Checksum(context.Background(), Resolved{SHA256: strings.ToUpper(sumB)}); got != sumB {
		t.Errorf("direct = %q", got)
	}
}

func TestResolveGitLab(t *testing.T) {
	srv := serve(t, map[string][]byte{
		"/api/v4/projects/gitlab-org/cli/releases/permalink/latest": []byte(`{"tag_name":"v1.121.0","assets":{"links":[
			{"name":"glab_1.121.0_linux_amd64.deb","url":"https://x/deb","direct_asset_url":"https://x/d/deb"},
			{"name":"glab_1.121.0_linux_amd64.tar.gz","url":"https://x/tgz","direct_asset_url":"https://x/d/tgz"},
			{"name":"checksums.txt","url":"https://x/sums","direct_asset_url":"https://x/d/sums"}]}}`),
	})
	m := manifest.Manifest{Source: &manifest.Source{Type: "gitlab", Repository: "gitlab-org/cli", Host: srv.URL}}
	r, err := Resolve(context.Background(), m, "x64")
	if err != nil || r.URL != "https://x/d/tgz" || r.Version != "v1.121.0" || r.ChecksumURL != "https://x/d/sums" {
		t.Errorf("Resolve = %+v, %v", r, err)
	}
}

func TestResolveAPIChecksumQuery(t *testing.T) {
	srv := serve(t, map[string][]byte{"/rel": []byte(`{"link":"https://x/app.tar.gz","sha":"` + sumA + `","sumlink":"https://x/app.tar.gz.sha256"}`)})
	for query, check := range map[string]func(Resolved) bool{
		".sha":     func(r Resolved) bool { return r.SHA256 == sumA },
		".sumlink": func(r Resolved) bool { return r.ChecksumURL == "https://x/app.tar.gz.sha256" },
	} {
		m := manifest.Manifest{Source: &manifest.Source{Type: "api", Endpoint: srv.URL + "/rel", JqQuery: ".link", ChecksumQuery: query}}
		r, err := Resolve(context.Background(), m, "x64")
		if err != nil || !check(r) || !r.ChecksumRequired {
			t.Errorf("%s: %+v, %v", query, r, err)
		}
	}
	m := manifest.Manifest{Source: &manifest.Source{Type: "api", Endpoint: srv.URL + "/rel", JqQuery: ".link", ChecksumQuery: ".link | ascii_downcase | .[0:3]"}}
	if _, err := Resolve(context.Background(), m, "x64"); err == nil {
		t.Error("checksum_query returning junk: want error")
	}
}
