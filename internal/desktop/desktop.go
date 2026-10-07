package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ominous-Josef/dopt/internal/manifest"
)

var DesktopDir = "/usr/share/applications"

func CreateDesktopEntry(m manifest.Manifest, installDir string, binLink string, realBinary string) {
	fmt.Println("[*] Scanning workspace assets for Application Desktop Graphics...")

	var iconPath string
	filepath.WalkDir(installDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			name := strings.ToLower(d.Name())
			if strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".svg") {
				if strings.Contains(name, "icon") || strings.Contains(name, strings.ToLower(m.AppID)) {
					iconPath = path
					return fmt.Errorf("found")
				}
			}
		}
		return nil
	})

	if iconPath == "" {
		iconPath = "system-run"
	}

	execLine := binLink
	if m.ExecFlags != "" {
		execLine += " " + m.ExecFlags
	}

	content := fmt.Sprintf(`[Desktop Entry]
Version=1.0
Type=Application
Name=%s
Comment=%s
Exec=%s
Icon=%s
Terminal=false
Categories=%s
StartupWMClass=%s
`, m.Name, m.Comment, execLine, iconPath, m.Categories, filepath.Base(realBinary))

	fmt.Printf("[*] Injecting desktop menu shell reference configuration at %s/%s.desktop...\n", DesktopDir, m.AppID)
	dest := filepath.Join(DesktopDir, fmt.Sprintf("%s.desktop", m.AppID))
	err := os.WriteFile(dest, []byte(content), 0644)
	if err != nil {
		fmt.Printf("[-] Warning: Failed to create desktop entry: %v\n", err)
	} else {
		fmt.Printf("[+] Native Desktop integration verified.\n")
	}
}
