package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ominous-Josef/dopt/internal/install"
	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/names"
	"github.com/Ominous-Josef/dopt/internal/registry"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

// askAppID prompts for an App ID; '?' lists what is installed.
func askAppID(u *ui.UI, l layout.Layout) (string, error) {
	u.Heading("Interactive setup")
	u.Detail("Tip: type '?' to list installed apps.")
	for {
		id, err := u.Ask("Enter App ID (e.g. com.example.app): ")
		if err != nil {
			return "", err
		}
		id = strings.TrimSpace(id)
		if id != "?" {
			if id == "" {
				return "", fmt.Errorf("an App ID is required")
			}
			return id, names.Validate("App ID", id)
		}
		listInstalled(u, l)
	}
}

// listInstalled prints registered apps and other folders in the opt folder.
func listInstalled(u *ui.UI, l layout.Layout) {
	fmt.Fprintf(u.Out, "\n--- Installed by dopt in %s ---\n", u.Path(l.OptDir))
	ids, _ := registry.List(l.RegistryDir)
	registered := map[string]bool{}
	for _, id := range ids {
		registered[id] = true
		if _, err := os.Stat(l.InstallDir(id)); err == nil {
			fmt.Fprintf(u.Out, "- %s\n", id)
		} else {
			fmt.Fprintf(u.Out, "- %s (folder missing)\n", id)
		}
	}
	if len(ids) == 0 {
		fmt.Fprintln(u.Out, "  (none registered yet)")
	}
	var others []string
	entries, _ := os.ReadDir(l.OptDir)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !registered[e.Name()] {
			others = append(others, e.Name())
		}
	}
	if len(others) > 0 {
		fmt.Fprintln(u.Out, "--- Other folders (not registered; older dopt installs or manual) ---")
		for _, o := range others {
			fmt.Fprintf(u.Out, "- %s\n", o)
		}
	}
	fmt.Fprint(u.Out, "--------------------------------------\n\n")
}

// previous holds the settings of an existing install, used as wizard defaults.
type previous struct {
	name, command, binary, icon, categories string
	cliOnly                                 string // "y", "n" or "" (unknown)
}

// readPrevious looks only where dopt itself writes: the registry, the command link and the launcher.
func readPrevious(l layout.Layout, appID string) previous {
	var p previous
	inst := l.InstallDir(appID)
	if _, err := os.Stat(inst); err != nil {
		return p
	}
	if e, _ := registry.Read(l.RegistryDir, appID); e != nil {
		p.command, p.binary = e[registry.KeyCommand], e[registry.KeyBinary]
	}
	if p.command == "" {
		if link := install.ExistingLink(l, appID); link != "" {
			p.command = link
			if target, err := os.Readlink(filepath.Join(l.BinDir, link)); err == nil {
				if rel, err := filepath.Rel(inst, target); err == nil && filepath.IsLocal(rel) {
					p.binary = rel
				}
			}
		}
	}
	if f, err := os.Open(l.DesktopFile(appID)); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), "=")
			if !ok {
				continue
			}
			switch {
			case k == "Name" && p.name == "":
				p.name = v
			case k == "Icon" && p.icon == "":
				p.icon = filepath.Base(v)
			case k == "Categories" && p.categories == "":
				p.categories = v
			}
		}
		p.cliOnly = "n"
	} else if p.command != "" {
		p.cliOnly = "y"
	}
	return p
}

// wizard asks for the app details that a manifest would provide.
// defName overrides the default name (used for "(Local)" copies).
func wizard(u *ui.UI, l layout.Layout, appID, symlinkCLI, defName string) (manifest.Manifest, error) {
	m := manifest.Manifest{AppID: appID}
	prev := readPrevious(l, appID)
	if _, err := os.Stat(l.InstallDir(appID)); err == nil {
		u.Detail("Existing install found; its settings are the defaults.")
	}
	or := func(vals ...string) string {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
		return ""
	}

	var err error
	def := or(defName, prev.name, appID)
	if m.Name, err = u.AskDefault(def, "Enter Application Name [%s]: ", def); err != nil {
		return m, err
	}
	m.Name = strings.TrimSpace(m.Name)

	if symlinkCLI != "" {
		m.SymlinkAs = symlinkCLI
	} else {
		def = or(prev.command, appID)
		if m.SymlinkAs, err = u.AskDefault(def, "Enter executable symlink name [%s]: ", def); err != nil {
			return m, err
		}
		m.SymlinkAs = strings.TrimSpace(m.SymlinkAs)
	}
	if err := names.Validate("command name", m.SymlinkAs); err != nil {
		return m, err
	}

	cliDefault := prev.cliOnly == "y"
	prompt := "Is this a CLI-only application? [y/N]: "
	if cliDefault {
		prompt = "Is this a CLI-only application? [Y/n]: "
	}
	cli, err := u.Confirm(cliDefault, "%s", prompt)
	if err != nil {
		return m, err
	}
	m.CliOnly = manifest.FlexBool(cli)

	def = or(prev.binary, m.SymlinkAs)
	bin, err := u.AskDefault(def, "Enter target binary name or relative path (e.g. bin/app) [%s]: ", def)
	if err != nil {
		return m, err
	}
	if bin = strings.TrimSpace(bin); strings.Contains(bin, "/") {
		m.BinaryPath = bin
	} else {
		m.BinaryPattern = bin
	}

	if !cli {
		if prev.icon != "" && prev.icon != "system-run" {
			m.IconPath, err = u.AskDefault(prev.icon, "Enter icon file path/name [%s]: ", prev.icon)
		} else {
			m.IconPath, err = u.Ask("Enter icon file path/name (leave blank to auto-detect): ")
		}
		if err != nil {
			return m, err
		}
		m.IconPath = strings.TrimSpace(m.IconPath)

		def = or(prev.categories, "Utility;")
		if m.Categories, err = u.AskDefault(def, "Enter Desktop Category (e.g. Utility;, Development;, Game;) [%s]: ", def); err != nil {
			return m, err
		}
		if m.Categories = strings.TrimSpace(m.Categories); !strings.HasSuffix(m.Categories, ";") {
			m.Categories += ";"
		}
	}
	m.ApplyDefaults()
	return m, m.Validate()
}
