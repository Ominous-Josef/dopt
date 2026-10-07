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
			u, err := Resolve(context.Background(), m, arch)
			if err != nil {
				t.Errorf("%s %s: %v", m.AppID, arch, err)
				continue
			}
			t.Logf("%-24s %-5s %s", m.AppID, arch, u)
		}
	}
}
