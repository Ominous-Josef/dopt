// Package install runs dopt's install/update pipeline:
//
//	check   ownership, registration and the command name
//	fetch   download (optional) and verify
//	unpack  extract, stage next to the live install, find the binary
//	install stop the running app, swap staged <-> live, register, link
//	desktop write the menu shortcut (GUI apps)
//
// The live install is only ever replaced by a pair of renames, and a failure
// at any point restores the previous version.
package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Ominous-Josef/dopt/internal/archive"
	"github.com/Ominous-Josef/dopt/internal/binfind"
	"github.com/Ominous-Josef/dopt/internal/desktop"
	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/linker"
	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/names"
	"github.com/Ominous-Josef/dopt/internal/owner"
	"github.com/Ominous-Josef/dopt/internal/proc"
	"github.com/Ominous-Josef/dopt/internal/registry"
	"github.com/Ominous-Josef/dopt/internal/source"
	"github.com/Ominous-Josef/dopt/internal/sysuser"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

// Error is a failure to report to the user. Code 0 means the user chose to stop.
type Error struct {
	Msg   string
	Hints []string
	Code  int
}

func (e *Error) Error() string { return e.Msg }

func fail(msg string, hints ...string) error { return &Error{Msg: msg, Hints: hints, Code: 1} }

// stopped means the user declined to continue; it exits 0.
func stopped(msg string) error { return &Error{Msg: msg, Code: 0} }

// Request describes one install or update.
type Request struct {
	Manifest     manifest.Manifest // complete: from a file, or filled in by the wizard
	ManifestPath string            // shown in resume hints; "" when the wizard was used
	Wizard       bool              // details came from prompts: offer the binary picker

	Archive  string                                // local archive (when not downloading)
	Download bool                                  // fetch the archive
	URL      string                                // download URL, or "" to call Resolve
	Resolve  func(context.Context) (string, error) // finds the URL from the manifest
	SHA256   string                                // expected checksum (lowercase hex), optional

	Cleanup        bool // delete the archive after a successful install
	Force          bool // -i: no confirmations; abort where a decision is needed
	SymlinkFromCLI bool // -s was given (kept in resume hints)
}

// Env is everything a run needs from the outside world.
type Env struct {
	Layout  layout.Layout
	User    sysuser.User
	AsRoot  bool
	UI      *ui.UI
	Program string // how the user invoked dopt, for hints
	Cwd     string // downloads are kept here
	PathEnv string // $PATH, for the "isn't on your PATH" note

	ShowProgress bool // draw download progress (stderr is a terminal)

	// Hooks; nil uses the real implementation.
	FindOwner func(dir string) (owner.Owner, bool)
	Lookup    func(name string) string
	Launch    func(binLink string) error
	Now       func() time.Time
}

func (e *Env) defaults() {
	if e.FindOwner == nil {
		e.FindOwner = owner.Find
	}
	if e.Lookup == nil {
		e.Lookup = func(name string) string { return linker.Lookup(name, e.User, e.AsRoot) }
	}
	if e.Launch == nil {
		e.Launch = func(binLink string) error { return proc.Launch(binLink, e.User, e.AsRoot) }
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Program == "" {
		e.Program = "dopt"
	}
}

type run struct {
	ctx context.Context
	env *Env
	req *Request
	m   *manifest.Manifest
	u   *ui.UI
	l   layout.Layout

	inst, stage, backup, binLink string

	work       string
	tarball    string
	url        string
	downloaded bool
	kept       bool
	swapping   bool // the live folder may be moved aside: restore it on failure
	renamed    bool
	declined   bool // the user kept the running app: offer a resume command

	mu sync.Mutex // held while the live folder is being swapped
}

// Run installs or updates req.Manifest.AppID.
func Run(ctx context.Context, env Env, req Request) (err error) {
	env.defaults()
	m := req.Manifest
	r := &run{ctx: ctx, env: &env, req: &req, m: &m, u: env.UI, l: env.Layout}
	r.inst = r.l.InstallDir(m.AppID)
	r.stage = r.l.StageDir(m.AppID)
	r.backup = r.l.BackupDir(m.AppID)

	if err := names.Validate("App ID", m.AppID); err != nil {
		return fail(err.Error())
	}
	if err := names.Validate("command name", m.SymlinkAs); err != nil {
		return fail(err.Error())
	}
	if err := r.l.Ensure(); err != nil {
		return fail(fmt.Sprintf("Couldn't create dopt's folders: %v", err))
	}

	stopSignals := r.handleSignals()
	defer stopSignals()
	defer func() { r.cleanup(err) }()

	// An update interrupted mid-swap left the previous version aside: put it back first.
	if exists(r.backup) && !exists(r.inst) {
		if err := r.assertManaged(r.backup); err != nil {
			return err
		}
		if err := os.Rename(r.backup, r.inst); err != nil {
			return fail(fmt.Sprintf("Couldn't restore the previous version from %s: %v", r.backup, err))
		}
		r.u.Warn("Restored %s, left aside by an interrupted update.", r.u.Path(r.inst))
	}

	steps := 3
	if req.Download {
		steps++
	}
	if !m.CliOnly {
		steps++
	}
	r.u.SetSteps(steps)
	wasInstalled := exists(r.inst)

	if err := r.check(wasInstalled); err != nil {
		return err
	}
	if err := r.fetch(); err != nil {
		return err
	}
	binaryRel, err := r.unpack()
	if err != nil {
		return err
	}
	restart, err := r.install(binaryRel)
	if err != nil {
		return err
	}
	realBinary := filepath.Join(r.inst, binaryRel)
	if err := r.shortcut(realBinary); err != nil {
		return err
	}
	note := r.archiveNote()
	r.summary(wasInstalled, note)
	r.launch(restart)
	return nil
}

// ---- 1. Check ----

func (r *run) check(wasInstalled bool) error {
	u, m := r.u, r.m
	u.Step("Checking %s...", m.Name)

	if wasInstalled {
		if o, ok := r.env.FindOwner(r.inst); ok {
			return fail(fmt.Sprintf("%s belongs to the system package '%s'. dopt won't modify package-managed files.", u.Path(r.inst), o.Package),
				"To use the archive version, install it alongside with a different App ID,",
				fmt.Sprintf("or remove the package first: sudo %s remove %s", o.Tool, o.Package))
		}
	}

	status, err := registry.Check(r.l.RegistryDir, m.AppID, r.inst)
	if err != nil {
		u.Warn("Couldn't read dopt's registry: %v", err)
	}
	if status == registry.Recreated || status == registry.Unregistered {
		if status == registry.Recreated {
			u.Warn("%s was replaced or recreated since dopt installed it.", u.Path(r.inst))
		} else {
			u.Warn("%s exists but isn't registered as a dopt install.", u.Path(r.inst))
			u.Detail("Installs made by older versions of dopt ask this once.")
		}
		r.describeFolder(r.inst)
		if r.req.Force {
			return fail("Refusing to replace it in forced (-i) mode. Re-run without -i to confirm.")
		}
		ok, err := u.Confirm(false, "Replace it with the new version? [y/N]: ")
		if err != nil || !ok {
			return stopped(fmt.Sprintf("Deployment aborted. %s was not modified.", u.Path(r.inst)))
		}
	}

	if err := r.chooseCommand(); err != nil {
		return err
	}
	if !m.CliOnly {
		if _, err := desktop.ExecPath(r.binLink); err != nil {
			return fail(err.Error())
		}
	}

	if wasInstalled {
		u.Detail("Updating the existing install at %s", u.Path(r.inst))
	} else if !r.req.Force {
		ok, err := u.Confirm(true, "Install %s to %s? [Y/n]: ", m.Name, u.Path(r.inst))
		if err != nil || !ok {
			return stopped("Deployment aborted.")
		}
	}
	return nil
}

func (r *run) describeFolder(dir string) {
	entries, _ := os.ReadDir(dir)
	r.u.Detail("Size: %s, %d top-level entries:", humanSize(dirSize(dir)), len(entries))
	for i, e := range entries {
		if i == 8 {
			fmt.Fprintln(r.u.Out, "        ...")
			break
		}
		fmt.Fprintf(r.u.Out, "        %s\n", e.Name())
	}
}

// chooseCommand settles the command link name: never a file dopt didn't
// create, and a warning (or choice) when the name exists elsewhere on PATH.
func (r *run) chooseCommand() error {
	u, m := r.u, r.m
	for {
		r.binLink = filepath.Join(r.l.BinDir, m.SymlinkAs)

		if linker.Inspect(r.binLink, r.inst) == linker.Foreign {
			u.Warn("%s already exists and wasn't installed by dopt for %s.", u.Path(r.binLink), m.AppID)
			if r.req.Force {
				return r.nameClashAbort()
			}
			fmt.Fprintln(u.Out, "    1) Choose a different command name")
			fmt.Fprintln(u.Out, "    2) Abort")
			choice, err := u.AskDefault("1", "Choose an action [1-2] (default 1): ")
			if err != nil || strings.TrimSpace(choice) != "1" {
				return r.nameClashAbort()
			}
			if err := r.askNewName(); err != nil {
				return err
			}
			continue
		}

		existing := r.env.Lookup(m.SymlinkAs)
		if strings.HasPrefix(existing, "/") && existing != r.binLink {
			if r.sameApp(existing) {
				u.Warn("'%s' also exists at %s (another install of this app). Whichever comes first in PATH runs.", m.SymlinkAs, u.Path(existing))
			} else {
				u.Warn("The command '%s' already exists at %s and wasn't installed by dopt.", m.SymlinkAs, u.Path(existing))
				if r.req.Force {
					return r.nameClashAbort()
				}
				fmt.Fprintln(u.Out, "    1) Choose a different command name")
				fmt.Fprintf(u.Out, "    2) Continue anyway (which '%s' runs will depend on PATH order)\n", m.SymlinkAs)
				fmt.Fprintln(u.Out, "    3) Abort")
				choice, err := u.AskDefault("1", "Choose an action [1-3] (default 1): ")
				if err != nil {
					return r.nameClashAbort()
				}
				switch strings.TrimSpace(choice) {
				case "1":
					if err := r.askNewName(); err != nil {
						return err
					}
					continue
				case "2":
					u.Warn("Continuing: %s will coexist with %s.", u.Path(r.binLink), u.Path(existing))
				default:
					return r.nameClashAbort()
				}
			}
		}
		break
	}
	if r.renamed && r.req.ManifestPath != "" {
		u.Detail("Tip: the manifest's 'symlink_as' doesn't match the name you chose. Update it, or pass -s %s next time.", shellQuote(m.SymlinkAs))
	}
	return nil
}

// sameApp reports whether path belongs to another install of this app (global, local or -local copy).
func (r *run) sameApp(path string) bool {
	base := strings.TrimSuffix(r.m.AppID, "-local")
	userOpt := r.l.OptDir
	if r.l.Global {
		userOpt = filepath.Join(r.env.User.Home, ".local", "opt")
	}
	for _, dir := range []string{r.inst, filepath.Join(r.l.GlobalOptDir, base), filepath.Join(userOpt, base), filepath.Join(userOpt, base+"-local")} {
		if linker.PathInside(path, dir) {
			return true
		}
	}
	return false
}

func (r *run) askNewName() error {
	for {
		name, err := r.u.Ask("Enter a different command name: ")
		if err != nil {
			return r.nameClashAbort()
		}
		name = strings.TrimSpace(name)
		switch {
		case name == "":
			r.u.Error("Name cannot be empty.")
		case !names.Valid(name):
			r.u.Error("Invalid name '%s' (%s).", name, names.Rules)
		default:
			r.m.SymlinkAs = name
			r.renamed = true
			return nil
		}
	}
}

func (r *run) nameClashAbort() error {
	if r.req.Force {
		r.u.Error("Can't ask for a different command name in forced (-i) mode.")
	}
	return fail("Deployment aborted. Re-run with -s <name> to use a different command name.")
}

// ---- 2. Fetch ----

func (r *run) fetch() error {
	work, err := os.MkdirTemp("", "dopt-workspace-")
	if err != nil {
		return fail(fmt.Sprintf("Couldn't create a work folder: %v", err))
	}
	r.work = work

	if !r.req.Download {
		r.tarball = r.req.Archive
		return nil
	}
	u := r.u
	u.Step("Downloading...")
	r.url = r.req.URL
	if r.url == "" && r.req.Resolve != nil {
		if r.url, err = r.req.Resolve(r.ctx); err != nil {
			return fail(fmt.Sprintf("Couldn't find the download: %v", err))
		}
		u.Detail("From %s", redactURL(r.url))
	}
	if r.url == "" {
		return fail("No download URL. Pass -u <url>, or add default_url_x64/default_url_arm64 or a source to the manifest.")
	}
	r.tarball = filepath.Join(r.work, "source_package.tar.gz")
	var progress source.Progress
	if r.env.ShowProgress {
		progress = func(done, total int64) {
			if total > 0 {
				fmt.Fprintf(u.Err, "\r      %s / %s (%d%%)  ", humanSize(done), humanSize(total), done*100/total)
			} else {
				fmt.Fprintf(u.Err, "\r      %s  ", humanSize(done))
			}
		}
	}
	err = source.Download(r.ctx, r.url, r.tarball, progress)
	if r.env.ShowProgress {
		fmt.Fprintln(u.Err)
	}
	if err != nil {
		return fail(fmt.Sprintf("Download failed. Check the URL and your connection: %s", redactURL(r.url)), err.Error())
	}
	r.downloaded = true
	return nil
}

// ---- 3. Unpack ----

func (r *run) unpack() (string, error) {
	u, m := r.u, r.m
	u.Step("Unpacking...")
	if r.req.SHA256 != "" {
		actual, err := source.VerifySHA256(r.tarball, r.req.SHA256)
		if errors.Is(err, source.ErrChecksum) {
			// A mismatched download must never be kept or offered for resuming.
			if r.downloaded {
				os.Remove(r.tarball)
			}
			return "", fail("SHA-256 mismatch. The archive was not installed.",
				"Expected: "+r.req.SHA256, "Actual:   "+actual)
		}
		if err != nil {
			return "", fail(fmt.Sprintf("Couldn't read the archive: %v", err))
		}
		u.Detail("SHA-256 verified")
	}

	extracted := filepath.Join(r.work, "extract")
	if err := os.Mkdir(extracted, 0o755); err != nil {
		return "", fail(err.Error())
	}
	if err := archive.Extract(r.tarball, extracted); err != nil {
		return "", fail(fmt.Sprintf("Couldn't unpack the archive: %v", err))
	}
	root, err := archive.InstallRoot(extracted)
	if errors.Is(err, archive.ErrEmpty) {
		return "", fail("The archive is empty.")
	}
	if err != nil {
		return "", fail(err.Error())
	}

	if unix.Access(r.l.OptDir, unix.W_OK) != nil {
		hints := []string{}
		if r.l.OptDir == "/opt" {
			hints = append(hints, "This is a system-wide install. Run dopt with sudo and --global.")
		}
		return "", fail(fmt.Sprintf("Permission denied: you can't write to %s.", r.l.OptDir), hints...)
	}
	if err := r.assertManaged(r.stage); err != nil {
		return "", err
	}
	if err := r.assertManaged(r.backup); err != nil {
		return "", err
	}
	// Clear leftovers of an interrupted run.
	if err := os.RemoveAll(r.stage); err != nil {
		return "", fail(err.Error())
	}
	if err := os.RemoveAll(r.backup); err != nil {
		return "", fail(err.Error())
	}
	if err := moveTree(root, r.stage); err != nil {
		return "", fail(fmt.Sprintf("Couldn't stage the new version: %v", err))
	}

	staged := ""
	if m.BinaryPath != "" {
		staged = filepath.Join(r.stage, m.BinaryPath)
	} else {
		staged = binfind.ByPattern(r.stage, m.BinaryPattern)
		if staged == "" {
			staged = binfind.FirstExecutable(r.stage)
		}
	}
	if staged == "" || !binfind.Inside(staged, r.stage) {
		staged = r.pickBinary()
	}
	if staged == "" {
		return "", fail("Couldn't find the app's executable in the archive. Nothing was changed.")
	}
	rel, _ := filepath.Rel(r.stage, staged)
	return rel, nil
}

func (r *run) pickBinary() string {
	u, m := r.u, r.m
	want := m.BinaryPath
	if want == "" {
		want = m.BinaryPattern
	}
	candidates := binfind.Candidates(r.stage, 10)
	u.Warn("Couldn't find the binary '%s' in the archive.", want)
	switch {
	case len(candidates) == 0:
		return ""
	case r.req.Wizard && !r.req.Force:
		u.Detail("Executables found in the package:")
		for i, c := range candidates {
			fmt.Fprintf(u.Out, "        %d) %s\n", i+1, c)
		}
		ans, err := u.Ask("Pick the binary to link [1-%d], or press Enter to abort: ", len(candidates))
		if err != nil {
			return ""
		}
		if n, err := strconv.Atoi(strings.TrimSpace(ans)); err == nil && n >= 1 && n <= len(candidates) {
			return filepath.Join(r.stage, candidates[n-1])
		}
		return ""
	case r.req.ManifestPath != "":
		u.Detail("Executables found in the package (use one as 'binary_path' in the manifest):")
	default:
		u.Detail("Executables found in the package (enter one as the binary path):")
	}
	for _, c := range candidates {
		fmt.Fprintf(u.Out, "        %s\n", c)
	}
	return ""
}

// assertManaged refuses to modify anything but <opt dir>/<app_id> and its two staging siblings.
func (r *run) assertManaged(p string) error { return assertManaged(r.l, r.m.AppID, p) }

func assertManaged(l layout.Layout, appID, p string) error {
	target := linker.Resolve(p)
	name := filepath.Base(target)
	if filepath.Dir(target) != linker.Resolve(l.OptDir) ||
		(name != appID && name != "."+appID+".dopt-new" && name != "."+appID+".dopt-old") {
		return fail(fmt.Sprintf("Safety abort: refusing to modify %s (outside dopt's folder %s).", target, l.OptDir))
	}
	return nil
}

// ---- 4. Install ----

func (r *run) install(binaryRel string) (restart bool, err error) {
	u, m := r.u, r.m
	u.Step("Installing...")

	if pids := proc.FindPIDs(r.inst, r.binLink, r.env.User.UID); len(pids) > 0 {
		if r.req.Force {
			u.Detail("Stopping the running %s (-i)...", m.Name)
		} else {
			u.Warn("%s is running.", m.Name)
			ok, err := u.Confirm(true, "Stop it to install the update? [Y/n]: ")
			if err != nil || !ok {
				u.Warn("Update canceled to keep %s running.", m.Name)
				r.declined = true
				return false, stopped("")
			}
		}
		proc.Terminate(pids)
		restart = !bool(m.CliOnly)
	}

	if err := r.assertManaged(r.inst); err != nil {
		return false, err
	}
	previous := r.previousCommand()
	if err := r.swap(); err != nil {
		return false, err
	}

	entry := registry.Entry{
		registry.KeyAppID:     m.AppID,
		registry.KeyInstalled: r.env.Now().Format(time.RFC3339),
		registry.KeyBinary:    binaryRel,
		registry.KeyCommand:   m.SymlinkAs,
	}
	if id, err := registry.FolderIdentity(r.inst); err == nil {
		entry[registry.KeyFolderID] = id
	}
	if src := r.sourceRecord(); src != "" {
		entry[registry.KeySource] = src
	}
	if err := registry.Write(r.l.RegistryDir, m.AppID, entry); err != nil {
		u.Warn("Couldn't record the install in dopt's registry (%v). The next update will ask before replacing it.", err)
	}

	if previous != "" && previous != m.SymlinkAs {
		if removed, _ := linker.RemoveIfOurs(filepath.Join(r.l.BinDir, previous), r.inst); removed {
			u.Detail("Removed the old command link '%s'", previous)
		}
	}
	realBinary := filepath.Join(r.inst, binaryRel)
	if info, err := os.Stat(realBinary); err == nil {
		os.Chmod(realBinary, info.Mode().Perm()|0o111)
	}
	if err := linker.Link(realBinary, r.binLink); err != nil {
		return false, fail(fmt.Sprintf("Couldn't create the command link %s: %v", r.binLink, err))
	}
	return restart, nil
}

// swap replaces the live folder with the staged one: live -> backup, staged -> live, drop backup.
func (r *run) swap() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.swapping = true
	if exists(r.inst) {
		if err := os.Rename(r.inst, r.backup); err != nil {
			return fail(fmt.Sprintf("Couldn't move the current version aside: %v", err))
		}
	}
	if err := os.Rename(r.stage, r.inst); err != nil {
		return fail(fmt.Sprintf("Couldn't move the new version into place: %v", err))
	}
	os.RemoveAll(r.backup)
	return nil
}

// previousCommand is the command link name of the existing install, if any.
func (r *run) previousCommand() string {
	if e, _ := registry.Read(r.l.RegistryDir, r.m.AppID); e[registry.KeyCommand] != "" {
		return e[registry.KeyCommand]
	}
	return ExistingLink(r.l, r.m.AppID)
}

// ExistingLink returns the name of a command link in the bin folder that points into appID's install.
func ExistingLink(l layout.Layout, appID string) string {
	entries, err := os.ReadDir(l.BinDir)
	if err != nil {
		return ""
	}
	inst := l.InstallDir(appID)
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 && linker.Inspect(filepath.Join(l.BinDir, e.Name()), inst) == linker.Ours {
			return e.Name()
		}
	}
	return ""
}

func (r *run) sourceRecord() string {
	src := r.url
	if !r.req.Download {
		if abs, err := filepath.Abs(r.req.Archive); err == nil {
			src = abs
		}
	}
	src = redactURL(src)
	if strings.ContainsAny(src, "\n\r") {
		return ""
	}
	return src
}

// ---- 5. Desktop shortcut ----

func (r *run) shortcut(realBinary string) error {
	u, m := r.u, r.m
	file := r.l.DesktopFile(m.AppID)
	if m.CliOnly {
		if removed, _ := desktop.Remove(file); removed {
			u.Detail("Removed the old menu shortcut (the app is CLI-only now)")
		}
		return nil
	}
	u.Step("Creating menu shortcut...")
	err := desktop.Write(file, desktop.Entry{
		Name:       m.Name,
		Comment:    m.Comment,
		Command:    r.binLink,
		Flags:      m.ExecFlags,
		Icon:       desktop.FindIcon(r.inst, m.IconPath, m.AppID, m.SymlinkAs),
		Categories: m.Categories,
		WMClass:    filepath.Base(realBinary),
	})
	if err != nil {
		return fail(fmt.Sprintf("Couldn't write the menu shortcut %s: %v", file, err))
	}
	if msg := desktop.Validate(file); msg != "" {
		u.Warn("desktop-file-validate reported:")
		for _, line := range strings.Split(msg, "\n") {
			u.Line("%s", line)
		}
	}
	return nil
}

// ---- After install ----

func (r *run) archiveNote() string {
	u := r.u
	if r.downloaded {
		if r.req.Cleanup {
			return "not kept (-c)"
		}
		saved, err := r.keep()
		if err != nil {
			return ""
		}
		return "kept at " + u.Path(saved)
	}
	if !r.req.Cleanup || !exists(r.tarball) {
		return ""
	}
	del := true
	if !r.req.Force {
		ok, err := u.Confirm(true, "Delete the archive %s? [Y/n]: ", u.Path(r.tarball))
		del = err == nil && ok
	}
	if !del {
		return "kept at " + u.Path(r.tarball)
	}
	if err := os.Remove(r.tarball); err != nil {
		u.Warn("Couldn't delete %s: %v", u.Path(r.tarball), err)
		return "kept at " + u.Path(r.tarball)
	}
	return fmt.Sprintf("deleted (%s)", u.Path(r.tarball))
}

func (r *run) keep() (string, error) {
	uid, gid := -1, -1
	if r.env.AsRoot {
		uid, gid = r.env.User.UID, r.env.User.GID
	}
	saved, err := source.Keep(r.tarball, r.url, r.m.AppID, r.env.Cwd, uid, gid)
	if err == nil {
		r.kept = true
	}
	return saved, err
}

func (r *run) summary(wasInstalled bool, archiveNote string) {
	u, m := r.u, r.m
	u.Blank()
	if wasInstalled {
		u.OK("%s updated", m.Name)
	} else {
		u.OK("%s installed", m.Name)
	}
	u.Line("Location   %s  (%s)", u.Path(r.inst), humanSize(dirSize(r.inst)))
	if !r.l.Global && !linker.OnPath(r.l.BinDir, r.env.PathEnv) {
		u.Line("Command    %s  (note: %s isn't on your PATH)", m.SymlinkAs, u.Path(r.l.BinDir))
	} else {
		u.Line("Command    %s", m.SymlinkAs)
	}
	if m.CliOnly {
		u.Line("Shortcut   none (CLI-only)")
	} else {
		u.Line("Shortcut   %s", desktop.Text(m.Name))
	}
	if archiveNote != "" {
		u.Line("Archive    %s", archiveNote)
	}
}

func (r *run) launch(restart bool) {
	u, m := r.u, r.m
	if m.CliOnly {
		return
	}
	if r.req.Force {
		if restart {
			if err := r.env.Launch(r.binLink); err != nil {
				u.Warn("Couldn't relaunch %s: %v", m.Name, err)
				return
			}
			u.OK("Relaunched %s", m.Name)
		}
		return
	}
	u.Blank()
	if ok, err := u.Confirm(true, "Launch %s now? [Y/n]: ", m.Name); err != nil || !ok {
		return
	}
	if err := r.env.Launch(r.binLink); err != nil {
		u.Warn("Couldn't launch %s: %v", m.Name, err)
		return
	}
	u.OK("Launched %s", m.Name)
}

// ---- Failure handling ----

// cleanup restores an interrupted swap, drops the staging copy and work folder,
// and keeps a downloaded archive (with a resume command) when the run failed.
func (r *run) cleanup(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.swapping && exists(r.backup) && !exists(r.inst) {
		if os.Rename(r.backup, r.inst) == nil {
			r.u.Warn("Update failed. The previous version was restored at %s", r.u.Path(r.inst))
		}
	}
	if r.stage != "" && exists(r.stage) && r.assertManaged(r.stage) == nil {
		os.RemoveAll(r.stage)
	}
	if err != nil {
		switch {
		case r.downloaded && !r.req.Cleanup && !r.kept:
			if saved, kerr := r.keep(); kerr == nil {
				r.u.Detail("The downloaded archive was kept at %s", r.u.Path(saved))
				r.resumeHint(saved)
			}
		case r.declined && !r.req.Download:
			r.resumeHint(r.tarball)
		}
	}
	if r.work != "" {
		os.RemoveAll(r.work)
		r.work = ""
	}
}

func (r *run) resumeHint(file string) {
	parts := []string{r.env.Program, "install"}
	if r.l.Global {
		parts = append([]string{"sudo"}, append(parts, "-g")...)
	}
	if r.req.ManifestPath != "" {
		parts = append(parts, "-m", shellQuote(r.req.ManifestPath))
	} else {
		parts = append(parts, "-a", shellQuote(r.m.AppID))
	}
	parts = append(parts, "-f", shellQuote(file))
	if r.req.SymlinkFromCLI || r.renamed {
		parts = append(parts, "-s", shellQuote(r.m.SymlinkAs))
	}
	if r.req.SHA256 != "" {
		parts = append(parts, "--sha256", r.req.SHA256)
	}
	r.u.Blank()
	r.u.Warn("To apply this update later without re-downloading, run:")
	fmt.Fprintf(r.u.Out, "    %s\n", strings.Join(parts, " "))
}

// handleSignals makes Ctrl-C (or SIGTERM) restore the previous version before exiting.
func (r *run) handleSignals() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-ch:
			fmt.Fprintln(r.u.Err)
			r.u.Error("Interrupted.")
			r.cleanup(errors.New("interrupted"))
			code := 130
			if sig == syscall.SIGTERM {
				code = 143
			}
			os.Exit(code)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
