package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/Ominous-Josef/dopt/internal/install"
	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/registry"
	"github.com/Ominous-Josef/dopt/internal/source"
	"github.com/Ominous-Josef/dopt/internal/sysuser"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

// printStatuses shows a table of update checks, with reasons below it.
func printStatuses(u *ui.UI, statuses []install.Status) {
	tw := tabwriter.NewWriter(u.Out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "  APP ID\tINSTALLED\tAVAILABLE\tSTATUS")
	for _, st := range statuses {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", st.AppID, orDash(st.Installed), orDash(st.Available), st.State)
	}
	tw.Flush()
	for _, st := range statuses {
		if st.Reason != "" {
			u.Detail("%s: %s", st.AppID, st.Reason)
		}
	}
}

func checkApps(u *ui.UI, l layout.Layout, ids []string) ([]install.Status, error) {
	arch, err := manifest.HostArch()
	if err != nil {
		return nil, err
	}
	source.UserAgent = "dopt/" + Version
	if len(ids) == 1 {
		u.Detail("Checking %s for updates...", ids[0])
	} else {
		u.Detail("Checking %d apps for updates...", len(ids))
	}
	return install.CheckAll(context.Background(), l, ids, arch), nil
}

func runOutdated(args []string, u *ui.UI) error {
	var global bool
	fs := flag.NewFlagSet("outdated", flag.ContinueOnError)
	fs.BoolVar(&global, "g", false, "")
	fs.BoolVar(&global, "global", false, "")
	rest, err := parseFlags(fs, args)
	if err != nil {
		return usagef("%v", err)
	}
	usr, err := sysuser.Real()
	if err != nil {
		return err
	}
	u.Home = usr.Home
	l, err := layout.View(global, usr.Home, os.Getenv(layout.TestRootEnv))
	if err != nil {
		return err
	}
	ids, err := targets(l, rest, len(rest) == 0)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Fprintf(u.Out, "Nothing installed by dopt in %s yet.\n", u.Path(l.OptDir))
		return nil
	}
	statuses, err := checkApps(u, l, ids)
	if err != nil {
		return err
	}
	printStatuses(u, statuses)
	n := 0
	for _, st := range statuses {
		if st.State == install.UpdateAvailable {
			n++
		}
	}
	u.Blank()
	switch n {
	case 0:
		u.OK("Everything that can be checked is up to date.")
	case 1:
		u.OK("1 update available. Run 'dopt update --all' to install it.")
	default:
		u.OK("%d updates available. Run 'dopt update --all' to install them.", n)
	}
	return nil
}

// targets is the registered apps named in args, or all of them.
func targets(l layout.Layout, args []string, all bool) ([]string, error) {
	registered, err := registry.List(l.RegistryDir)
	if err != nil {
		return nil, err
	}
	if all {
		return registered, nil
	}
	known := map[string]bool{}
	for _, id := range registered {
		known[id] = true
	}
	for _, id := range args {
		if !known[id] {
			return nil, fmt.Errorf("%s isn't installed by dopt in %s. Run 'dopt list' to see installed apps", id, l.OptDir)
		}
	}
	return args, nil
}

func runUpdate(args []string, u *ui.UI) error {
	var global, force, keep, all bool
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.BoolVar(&global, "g", false, "")
	fs.BoolVar(&global, "global", false, "")
	fs.BoolVar(&force, "i", false, "")
	fs.BoolVar(&force, "install", false, "")
	fs.BoolVar(&force, "y", false, "")
	fs.BoolVar(&force, "yes", false, "")
	fs.BoolVar(&keep, "k", false, "")
	fs.BoolVar(&keep, "keep", false, "")
	fs.BoolVar(&all, "all", false, "")
	rest, err := parseFlags(fs, args)
	if err != nil {
		return usagef("%v", err)
	}
	if all == (len(rest) > 0) {
		return usagef("name the apps to update, or use --all")
	}
	usr, err := sysuser.Real()
	if err != nil {
		return err
	}
	u.Home = usr.Home
	euid := os.Geteuid()
	l, err := layout.Resolve(global, usr.Home, euid, os.Getenv(layout.TestRootEnv))
	if err != nil {
		return err
	}
	ids, err := targets(l, rest, all)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Fprintf(u.Out, "Nothing installed by dopt in %s yet.\n", u.Path(l.OptDir))
		return nil
	}
	statuses, err := checkApps(u, l, ids)
	if err != nil {
		return err
	}

	// --all updates what is known to be outdated; named apps are updated unless known to be current.
	var todo []install.Status
	failed := 0
	for _, st := range statuses {
		switch {
		case st.State == install.UpdateAvailable, !all && st.State == install.Unknown:
			todo = append(todo, st)
		case !all && st.State == install.UpToDate:
			u.OK("%s is up to date (%s).", st.AppID, orDash(st.Installed))
		case !all && st.State == install.Unavailable:
			u.Error("Can't update %s: %s.", st.AppID, st.Reason)
			failed++
		}
	}
	if all {
		printStatuses(u, statuses)
	}
	if len(todo) == 0 {
		if all {
			u.Blank()
			u.OK("Nothing to update.")
		}
		if failed > 0 {
			return &install.Error{Code: 1}
		}
		return nil
	}

	u.Blank()
	u.Heading("Updates:")
	for _, st := range todo {
		from, to := orDash(st.Installed), orDash(st.Available)
		fmt.Fprintf(u.Out, "  %s  %s -> %s\n", st.AppID, from, to)
	}
	if !force {
		prompt := fmt.Sprintf("Update %d apps? [Y/n]: ", len(todo))
		if len(todo) == 1 {
			prompt = fmt.Sprintf("Update %s? [Y/n]: ", todo[0].Name)
		}
		if ok, err := u.Confirm(true, "%s", prompt); err != nil || !ok {
			return &install.Error{Msg: "Nothing was updated.", Code: 0}
		}
	}

	env, err := newEnv(l, usr, euid, u)
	if err != nil {
		return err
	}
	updated := 0
	for _, st := range todo {
		u.Blank()
		u.Heading("==> %s", st.Name)
		req := install.UpdateRequest(l, st)
		req.Cleanup, req.Force = !keep, force
		err := install.Run(context.Background(), env, req)
		var ie *install.Error
		switch {
		case err == nil:
			updated++
		case errors.As(err, &ie) && ie.Code == 0:
			report(u, err) // the user chose to skip this one
		default:
			report(u, err)
			failed++
		}
	}
	u.Blank()
	if updated > 0 {
		u.OK("Updated %d of %d.", updated, len(todo))
	}
	if failed > 0 {
		return &install.Error{Msg: fmt.Sprintf("%d update(s) failed.", failed), Code: 1}
	}
	return nil
}
