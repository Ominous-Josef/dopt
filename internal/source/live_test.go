//go:build live

// Resolves the real recipes against the real vendor endpoints.
// Run with: go test -tags live ./internal/source
package source

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Ominous-Josef/dopt/internal/manifest"
)

func TestLiveRecipes(t *testing.T) {
	files, _ := filepath.Glob("../../recipes/*.json")
	for _, f := range files {
		m, _, err := manifest.Load(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		for _, arch := range []string{"x64", "arm64"} {
			if !m.HasDownload(arch) {
				t.Logf("%-24s %-5s (no download source)", m.AppID, arch)
				continue
			}
			r, err := Resolve(context.Background(), m, arch)
			if err != nil {
				t.Errorf("%s %s: %v", m.AppID, arch, err)
				continue
			}
			info, err := Probe(context.Background(), r.URL)
			if err != nil {
				t.Errorf("%s %s: probe: %v", m.AppID, arch, err)
				continue
			}
			fp, reliable := Fingerprint(info)
			t.Logf("%-18s %-5s version=%-12q reliable=%-5v %s\n%30s fingerprint %s", m.AppID, arch, Version(r, info), reliable, r.URL, "", fp)
		}
	}
}
