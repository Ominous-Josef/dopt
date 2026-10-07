package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ominous-Josef/dopt/internal/install"
	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/names"
	"github.com/Ominous-Josef/dopt/internal/sysuser"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

// errAborted is the user choosing "Abort" from a menu (exit 1, like dopt-bash).
var errAborted = &install.Error{Msg: "Deployment aborted.", Code: 1}

// globalChoice handles a local install of an app that also exists system-wide:
// update the system-wide copy (re-run with sudo), install a separate local copy, or abort.
// It returns the (possibly "-local") App ID and the default name for that copy.
func globalChoice(u *ui.UI, l layout.Layout, appID string, o InstallOptions, wizard bool) (string, string, error) {
	globalDir := filepath.Join(l.GlobalOptDir, appID)
	if l.Global {
		return appID, "", nil
	}
	if info, err := os.Stat(globalDir); err != nil || !info.IsDir() {
		return appID, "", nil
	}
	u.Blank()
	u.Warn("Found existing system-wide installation of %s at %s.", appID, globalDir)
	if o.Force {
		return "", "", &install.Error{Code: 1,
			Msg:   "Can't choose between the system-wide and a local install in forced (-i) mode.",
			Hints: []string{"Add -g to update the system-wide install (with sudo), or pass a different App ID."}}
	}
	fmt.Fprintln(u.Out, "    1) Update the system-wide install (re-runs with sudo)")
	fmt.Fprintln(u.Out, "    2) Install a separate local copy")
	fmt.Fprintln(u.Out, "    3) Abort")
	choice, err := u.Ask("Choose an action [1-3]: ")
	if err != nil {
		return "", "", errAborted
	}
	switch strings.TrimSpace(choice) {
	case "1":
		u.Detail("Re-running with sudo...")
		return "", "", reexecWithSudo(o, appID, wizard)
	case "2":
		ok, err := u.Confirm(true, "Add '-local' to the App ID and name, so this copy doesn't hide the system-wide app in your menu? [Y/n]: ")
		if err != nil || !ok {
			return appID, "", nil
		}
		defName := ""
		if name := desktopName(filepath.Join(l.GlobalDesktopDir, appID+".desktop")); name != "" {
			defName = name + " (Local)"
		}
		localID := appID + "-local"
		if err := names.Validate("App ID", localID); err != nil {
			return "", "", err
		}
		u.Detail("App ID is now %s", localID)
		return localID, defName, nil
	}
	return "", "", errAborted
}

// desktopName reads Name= from a launcher.
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

// reexecWithSudo replaces dopt with `sudo dopt install -g <original options>`.
func reexecWithSudo(o InstallOptions, appID string, wizard bool) error {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return errors.New("sudo isn't available")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	argv := []string{"sudo", self, "install", "-g"}
	argv = append(argv, o.Args...)
	if wizard && o.AppID == "" {
		argv = append(argv, "-a", appID)
	}
	return syscall.Exec(sudo, argv, os.Environ())
}

// downloadsDir is the real user's downloads folder (XDG, so renamed or translated folders work).
func downloadsDir(l layout.Layout, usr sysuser.User, asRoot bool) string {
	if l.DownloadsDir != "" {
		return l.DownloadsDir
	}
	var out []byte
	if _, err := exec.LookPath("xdg-user-dir"); err == nil {
		if asRoot {
			out, _ = exec.Command("sudo", "-u", usr.Name, "xdg-user-dir", "DOWNLOAD").Output()
		} else {
			out, _ = exec.Command("xdg-user-dir", "DOWNLOAD").Output()
		}
	}
	dir := strings.TrimSpace(string(out))
	// xdg-user-dir falls back to $HOME when no downloads folder is configured.
	if dir == "" || dir == usr.Home || dir == os.Getenv("HOME") {
		dir = filepath.Join(usr.Home, "Downloads")
	}
	return dir
}

type recent struct {
	path  string
	mtime time.Time
}

// recentArchives lists the newest .tar.gz/.tgz files directly in dir.
func recentArchives(dir string, limit int) []recent {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []recent
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !(strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz")) {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, recent{filepath.Join(dir, name), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })
	if len(files) > limit {
		files = files[:limit]
	}
	return files
}

// ageText is a "2 days ago" style age.
func ageText(t, now time.Time) string {
	secs := int64(now.Sub(t).Seconds())
	if secs < 0 {
		secs = 0
	}
	switch {
	case secs < 3600:
		return fmt.Sprintf("%d min ago", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%d h ago", secs/3600)
	case secs < 86400*14:
		return fmt.Sprintf("%d days ago", secs/86400)
	}
	return fmt.Sprintf("%d weeks ago", secs/604800)
}

// pickSource asks for an archive when none was given: a recent download, a URL or a path.
// It never picks a file on its own.
func pickSource(u *ui.UI, dlDir string) (file, url string, err error) {
	files := recentArchives(dlDir, 5)
	u.Blank()
	u.Heading("No archive given.")
	if len(files) > 0 {
		fmt.Fprintf(u.Out, "    Recent downloads in %s:\n", dlDir)
		now := time.Now()
		for i, f := range files {
			fmt.Fprintf(u.Out, "    %d) %s  (%s)\n", i+1, filepath.Base(f.path), ageText(f.mtime, now))
		}
	}
	fmt.Fprintln(u.Out, "    u) Enter a URL   p) Enter a path   a) Abort")
	prompt := "Choose [u/p/a]: "
	if len(files) > 0 {
		prompt = fmt.Sprintf("Choose [1-%d/u/p/a]: ", len(files))
	}
	choice, err := u.Ask("%s", prompt)
	if err != nil {
		return "", "", errAborted
	}
	choice = strings.ToLower(strings.TrimSpace(choice))
	if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(files) {
		u.Detail("Using %s", u.Path(files[n-1].path))
		return files[n-1].path, "", nil
	}
	switch choice {
	case "u":
		url, err := u.Ask("Enter full URL (e.g. https://...): ")
		if url = strings.TrimSpace(url); err != nil || url == "" {
			return "", "", errors.New("the URL can't be empty")
		}
		return "", url, nil
	case "p":
		path, err := u.Ask("Enter the archive path: ")
		path = strings.TrimSpace(path)
		// Paths pasted from a file manager often come wrapped in quotes.
		if len(path) >= 2 && (path[0] == '\'' || path[0] == '"') && path[len(path)-1] == path[0] {
			path = path[1 : len(path)-1]
		}
		if err != nil || path == "" {
			return "", "", errors.New("the path can't be empty")
		}
		return path, "", nil
	}
	return "", "", errAborted
}
