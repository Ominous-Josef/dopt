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

	// App details: from the manifest, or asked by the wizard.
	var m manifest.Manifest
	if o.Manifest != "" {
		var warnings []string
		if m, warnings, err = manifest.Load(o.Manifest); err != nil {
			return fmt.Errorf("%s: %w", o.Manifest, err)
		}
		for _, w := range warnings {
			u.Warn("Manifest: %s", w)
		}
		if o.Symlink != "" {
			m.SymlinkAs = o.Symlink
		}
		req.ManifestPath = o.Manifest
	} else {
		appID := o.AppID
		if appID == "" {
			if appID, err = askAppID(u, l); err != nil {
				return err
			}
		} else if err := names.Validate("App ID", appID); err != nil {
			return err
		}
		if m, err = wizard(u, l, appID, o.Symlink, ""); err != nil {
			return err
		}
		req.Wizard = true
	}
	req.Manifest = m

	// The archive: a local file, or a download.
	switch {
	case o.File != "":
		path := expandHome(o.File, usr.Home)
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("archive not found: %s", path)
		}
		req.Archive = path
	case o.URL != "":
		req.Download, req.URL = true, o.URL
	case o.Download:
		if o.Manifest == "" || !m.HasDownload(arch) {
			return errors.New("no download URL. Pass -u <url>, or add default_url_x64/default_url_arm64 or a source to the manifest")
		}
		req.Download = true
		req.Resolve = func(ctx context.Context) (string, error) { return source.Resolve(ctx, m, arch) }
	default:
		return errors.New("no archive given. Pass -f <file>, -u <url> or -d")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	env := install.Env{
		Layout:       l,
		User:         usr,
		AsRoot:       euid == 0,
		UI:           u,
		Program:      os.Args[0],
		Cwd:          cwd,
		PathEnv:      os.Getenv("PATH"),
		ShowProgress: ui.IsTerminal(os.Stderr),
	}
	return install.Run(context.Background(), env, req)
}
