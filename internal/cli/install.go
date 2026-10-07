package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Ominous-Josef/dopt/internal/install"
	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/names"
	"github.com/Ominous-Josef/dopt/internal/source"
	"github.com/Ominous-Josef/dopt/internal/sysuser"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

// expandHome turns a leading ~ into the real user's home.
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return home + "/" + rest
	}
	return p
}

func runInstall(o InstallOptions, u *ui.UI) error {
	usr, err := sysuser.Real()
	if err != nil {
		return err
	}
	u.Home = usr.Home
	euid := os.Geteuid()
	l, err := layout.Resolve(o.Global, usr.Home, euid, os.Getenv(layout.TestRootEnv))
	if err != nil {
		return err
	}
	arch, err := manifest.HostArch()
	if err != nil {
		return err
	}
	source.UserAgent = "dopt/" + Version

	req := install.Request{
		SHA256:         o.SHA256,
		Cleanup:        o.Cleanup,
		Force:          o.Force,
		SymlinkFromCLI: o.Symlink != "",
	}
	if o.Symlink != "" {
		if err := names.Validate("command name", o.Symlink); err != nil {
			return err
		}
	}

	// 1. Which app: from the manifest, -a, or asked.
	var m manifest.Manifest
	appID := o.AppID
	if o.Manifest != "" {
		var warnings []string
		if m, warnings, err = manifest.Load(o.Manifest); err != nil {
			return fmt.Errorf("%s: %w", o.Manifest, err)
		}
		for _, w := range warnings {
			u.Warn("Manifest: %s", w)
		}
		appID = m.AppID
		req.ManifestPath = o.Manifest
	} else if appID == "" {
		if appID, err = askAppID(u, l); err != nil {
			return err
		}
	} else if err := names.Validate("App ID", appID); err != nil {
		return err
	}

	// 2. A system-wide copy exists: update it with sudo, or install a separate local copy.
	localID, localName, err := globalChoice(u, l, appID, o, o.Manifest == "")
	if err != nil {
		return err
	}
	isLocalCopy := localID != appID
	appID = localID

	// 3. The archive: a local file, a download, or chosen from recent downloads.
	file, url := o.File, o.URL
	if file == "" && url == "" && !o.Download {
		if o.Force {
			return errors.New("no archive given. Pass -f <file>, -u <url> or -d")
		}
		if file, url, err = pickSource(u, downloadsDir(l, usr, euid == 0)); err != nil {
			return err
		}
	}
	switch {
	case file != "":
		path := expandHome(file, usr.Home)
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("archive not found: %s", path)
		}
		req.Archive = path
	case url != "":
		req.Download, req.URL = true, url
	default: // -d
		if o.Manifest == "" || !m.HasDownload(arch) {
			return errors.New("no download URL. Pass -u <url>, or add default_url_x64/default_url_arm64 or a source to the manifest")
		}
		req.Download = true
		resolveFrom := m
		req.Resolve = func(ctx context.Context) (source.Resolved, error) { return source.Resolve(ctx, resolveFrom, arch) }
	}

	// 4. App details: the manifest's, or asked by the wizard.
	if o.Manifest == "" {
		if m, err = wizard(u, l, appID, o.Symlink, localName); err != nil {
			return err
		}
		req.Wizard = true
	}
	m.AppID = appID
	if o.Symlink != "" {
		m.SymlinkAs = o.Symlink
	}
	if isLocalCopy && !strings.HasSuffix(m.Name, " (Local)") {
		m.Name += " (Local)"
	}
	req.Manifest = m

	env, err := newEnv(l, usr, euid, u)
	if err != nil {
		return err
	}
	return install.Run(context.Background(), env, req)
}

// newEnv is the install environment for this process.
func newEnv(l layout.Layout, usr sysuser.User, euid int, u *ui.UI) (install.Env, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return install.Env{}, err
	}
	return install.Env{
		Layout:       l,
		User:         usr,
		AsRoot:       euid == 0,
		UI:           u,
		Program:      os.Args[0],
		Cwd:          cwd,
		PathEnv:      os.Getenv("PATH"),
		ShowProgress: ui.IsTerminal(os.Stderr),
	}, nil
}
