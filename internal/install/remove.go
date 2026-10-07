package install

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ominous-Josef/dopt/internal/desktop"
	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/linker"
	"github.com/Ominous-Josef/dopt/internal/names"
	"github.com/Ominous-Josef/dopt/internal/proc"
	"github.com/Ominous-Josef/dopt/internal/registry"
)

// Remove uninstalls appID: its folder, its command links and its menu shortcut.
// Only registered dopt installs are removed; anything else is left alone.
func Remove(env Env, appID string, force bool) error {
	env.defaults()
	u, l := env.UI, env.Layout
	if err := names.Validate("App ID", appID); err != nil {
		return fail(err.Error())
	}
	inst := l.InstallDir(appID)
	entry, err := registry.Read(l.RegistryDir, appID)
	if err != nil {
		return fail(fmt.Sprintf("Couldn't read dopt's registry: %v", err))
	}

	status, _ := registry.Check(l.RegistryDir, appID, inst)
	switch {
	case status == registry.Absent && entry == nil:
		return fail(fmt.Sprintf("%s isn't installed in %s.", appID, u.Path(l.OptDir)), "Run 'dopt list' to see installed apps.")
	case status == registry.Unregistered || status == registry.Recreated:
		return fail(fmt.Sprintf("%s isn't a registered dopt install (or was replaced since), so dopt won't delete it.", u.Path(inst)),
			"Remove it yourself if you're sure it's no longer needed.")
	}
	if status == registry.Registered {
		if o, ok := env.FindOwner(inst); ok {
			return fail(fmt.Sprintf("%s belongs to the system package '%s'. dopt won't modify package-managed files.", u.Path(inst), o.Package),
				fmt.Sprintf("Remove the package instead: sudo %s remove %s", o.Tool, o.Package))
		}
	}

	links := ourLinks(l, appID)
	name := desktopName(l.DesktopFile(appID))
	if name == "" {
		name = appID
	}

	if status == registry.Registered {
		u.Heading("Removing %s", name)
		u.Detail("Folder: %s (%s)", u.Path(inst), humanSize(dirSize(inst)))
	} else {
		u.Heading("Cleaning up %s (its folder is already gone)", name)
	}
	for _, link := range links {
		u.Detail("Command: %s", filepath.Base(link))
	}
	if !force {
		ok, err := u.Confirm(false, "Remove %s? [y/N]: ", name)
		if err != nil || !ok {
			return stopped("Nothing was removed.")
		}
	}

	if status == registry.Registered {
		binLink := ""
		if len(links) > 0 {
			binLink = links[0]
		}
		if pids := proc.FindPIDs(inst, binLink, env.User.UID); len(pids) > 0 {
			if !force {
				ok, err := u.Confirm(true, "%s is running. Stop it? [Y/n]: ", name)
				if err != nil || !ok {
					return stopped("Nothing was removed.")
				}
			}
			proc.Terminate(pids)
		}
		if err := deleteInstall(l, appID); err != nil {
			return err
		}
	}
	for _, link := range links {
		if removed, _ := linker.RemoveIfOurs(link, inst); removed {
			u.Detail("Removed the command link %s", filepath.Base(link))
		}
	}
	if launcherRuns(l.DesktopFile(appID), links) {
		if removed, _ := desktop.Remove(l.DesktopFile(appID)); removed {
			u.Detail("Removed the menu shortcut")
		}
	}
	if err := registry.Remove(l.RegistryDir, appID); err != nil {
		u.Warn("Couldn't remove the registry entry: %v", err)
	}
	u.OK("%s removed", name)
	return nil
}

// deleteInstall moves the folder aside (one rename, so the app disappears at once) and deletes it.
func deleteInstall(l layout.Layout, appID string) error {
	inst, aside := l.InstallDir(appID), l.BackupDir(appID)
	if err := assertManaged(l, appID, inst); err != nil {
		return err
	}
	if err := assertManaged(l, appID, aside); err != nil {
		return err
	}
	if err := os.RemoveAll(aside); err != nil {
		return fail(err.Error())
	}
	if err := os.Rename(inst, aside); err != nil {
		return fail(fmt.Sprintf("Couldn't remove %s: %v", inst, err))
	}
	if err := os.RemoveAll(aside); err != nil {
		return fail(fmt.Sprintf("Couldn't delete %s completely: %v", aside, err))
	}
	return nil
}

// ourLinks lists command links in the bin folder that point into appID's install folder.
func ourLinks(l layout.Layout, appID string) []string {
	entries, err := os.ReadDir(l.BinDir)
	if err != nil {
		return nil
	}
	inst := l.InstallDir(appID)
	var links []string
	for _, e := range entries {
		p := filepath.Join(l.BinDir, e.Name())
		if e.Type()&os.ModeSymlink != 0 && linker.Inspect(p, inst) == linker.Ours {
			links = append(links, p)
		}
	}
	return links
}

// launcherRuns reports whether the launcher's Exec runs one of links: proof that dopt wrote it.
func launcherRuns(file string, links []string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Exec="); ok {
			for _, link := range links {
				if quoted, err := desktop.ExecPath(link); err == nil && strings.HasPrefix(v, quoted) {
					return true
				}
			}
		}
	}
	return false
}

func desktopName(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Name="); ok {
			return v
		}
	}
	return ""
}

// App describes one app in the opt folder, for `dopt list`.
type App struct {
	ID         string
	Registered bool
	Missing    bool // registered, but the folder is gone
	Name       string
	Command    string
	Size       int64
	Installed  time.Time
}

// List returns registered apps, then other folders in the opt folder.
func List(l layout.Layout) []App {
	var apps []App
	ids, _ := registry.List(l.RegistryDir)
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
		a := App{ID: id, Registered: true, Name: desktopName(l.DesktopFile(id))}
		e, _ := registry.Read(l.RegistryDir, id)
		a.Command = e[registry.KeyCommand]
		if t, err := time.Parse(time.RFC3339, e[registry.KeyInstalled]); err == nil {
			a.Installed = t
		}
		if _, err := os.Stat(l.InstallDir(id)); err != nil {
			a.Missing = true
		} else {
			a.Size = dirSize(l.InstallDir(id))
			if a.Command == "" {
				if links := ourLinks(l, id); len(links) > 0 {
					a.Command = filepath.Base(links[0])
				}
			}
		}
		apps = append(apps, a)
	}
	entries, _ := os.ReadDir(l.OptDir)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !seen[e.Name()] {
			apps = append(apps, App{ID: e.Name()})
		}
	}
	return apps
}

// HumanSize formats a byte count like du -h.
func HumanSize(n int64) string { return humanSize(n) }
