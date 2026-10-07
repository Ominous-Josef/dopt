// Package layout decides where dopt writes: per-user folders under ~/.local by
// default, system folders with --global, or a throwaway root in test mode.
package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// TestRootEnv redirects every local-mode write into a throwaway folder (used by tests).
const TestRootEnv = "DOPT_TEST_ROOT"

// Layout holds the folders one dopt run reads and writes.
type Layout struct {
	Global bool

	OptDir      string // apps live in OptDir/<app_id>
	BinDir      string // command links
	DesktopDir  string // .desktop launchers
	RegistryDir string // OptDir/.dopt: one entry per dopt install

	// Where a system-wide install would live; checked when installing locally.
	GlobalOptDir     string
	GlobalDesktopDir string

	DownloadsDir string // offered when no archive is given; empty means "ask xdg-user-dir"
	TestRoot     string
}

// Resolve picks the layout for this run. Global mode requires root, local mode refuses it,
// and test mode (testRoot set) keeps every write under testRoot.
func Resolve(global bool, home string, euid int, testRoot string) (Layout, error) {
	if testRoot != "" && global {
		return Layout{}, fmt.Errorf("%s (test mode) can't be combined with --global", TestRootEnv)
	}
	if global {
		if euid != 0 {
			return Layout{}, errors.New("installing system-wide needs root. Run it again with sudo")
		}
		return withRegistry(Layout{
			Global:           true,
			OptDir:           "/opt",
			BinDir:           "/usr/local/bin",
			DesktopDir:       "/usr/share/applications",
			GlobalOptDir:     "/opt",
			GlobalDesktopDir: "/usr/share/applications",
		}), nil
	}
	if euid == 0 {
		return Layout{}, errors.New("don't run a personal install as root. Run it again without sudo, or add --global to install system-wide")
	}
	if testRoot != "" {
		return withRegistry(Layout{
			OptDir:           filepath.Join(testRoot, "opt"),
			BinDir:           filepath.Join(testRoot, "bin"),
			DesktopDir:       filepath.Join(testRoot, "applications"),
			GlobalOptDir:     filepath.Join(testRoot, "global-opt"),
			GlobalDesktopDir: filepath.Join(testRoot, "global-applications"),
			DownloadsDir:     filepath.Join(testRoot, "Downloads"),
			TestRoot:         testRoot,
		}), nil
	}
	return withRegistry(Layout{
		OptDir:           filepath.Join(home, ".local", "opt"),
		BinDir:           filepath.Join(home, ".local", "bin"),
		DesktopDir:       filepath.Join(home, ".local", "share", "applications"),
		GlobalOptDir:     "/opt",
		GlobalDesktopDir: "/usr/share/applications",
	}), nil
}

func withRegistry(l Layout) Layout {
	l.RegistryDir = filepath.Join(l.OptDir, ".dopt")
	return l
}

// Ensure creates the per-user folders. System folders are expected to exist already.
func (l Layout) Ensure() error {
	if l.Global {
		return nil
	}
	for _, d := range []string{l.OptDir, l.BinDir, l.DesktopDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// InstallDir is the only folder dopt ever installs into (and deletes) for appID.
func (l Layout) InstallDir(appID string) string { return filepath.Join(l.OptDir, appID) }

// StageDir holds the new version while it is checked, before the swap.
func (l Layout) StageDir(appID string) string { return filepath.Join(l.OptDir, "."+appID+".dopt-new") }

// BackupDir holds the previous version during the swap.
func (l Layout) BackupDir(appID string) string { return filepath.Join(l.OptDir, "."+appID+".dopt-old") }

// DesktopFile is the launcher path for appID.
func (l Layout) DesktopFile(appID string) string {
	return filepath.Join(l.DesktopDir, appID+".desktop")
}

// View is Resolve for read-only commands (like `dopt list`): it skips the root checks.
func View(global bool, home, testRoot string) (Layout, error) {
	euid := 1
	if global {
		euid = 0
	}
	return Resolve(global, home, euid, testRoot)
}
