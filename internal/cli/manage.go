package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/Ominous-Josef/dopt/internal/install"
	"github.com/Ominous-Josef/dopt/internal/layout"
	"github.com/Ominous-Josef/dopt/internal/sysuser"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

// parseFlags parses fs from args, allowing flags after positional arguments
// (`dopt remove app -i`), and returns the positional arguments.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func runList(args []string, u *ui.UI) error {
	var global bool
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.BoolVar(&global, "g", false, "")
	fs.BoolVar(&global, "global", false, "")
	rest, err := parseFlags(fs, args)
	if err != nil {
		return usagef("%v", err)
	}
	if len(rest) > 0 {
		return usagef("unexpected argument %q", rest[0])
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

	apps := install.List(l)
	var registered, others []install.App
	for _, a := range apps {
		if a.Registered {
			registered = append(registered, a)
		} else {
			others = append(others, a)
		}
	}
	if len(registered) == 0 {
		fmt.Fprintf(u.Out, "Nothing installed by dopt in %s yet.\n", u.Path(l.OptDir))
	} else {
		u.Heading("Installed by dopt in %s:", u.Path(l.OptDir))
		tw := tabwriter.NewWriter(u.Out, 0, 0, 3, ' ', 0)
		fmt.Fprintln(tw, "  APP ID\tNAME\tCOMMAND\tSIZE\tINSTALLED")
		for _, a := range registered {
			size, date := install.HumanSize(a.Size), "-"
			if a.Missing {
				size = "(folder missing)"
			}
			if !a.Installed.IsZero() {
				date = a.Installed.Local().Format("2006-01-02")
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", a.ID, orDash(a.Name), orDash(a.Command), size, date)
		}
		tw.Flush()
	}
	if len(others) > 0 {
		u.Blank()
		u.Heading("Other folders (not installed by this dopt, or by an older version):")
		for _, a := range others {
			fmt.Fprintf(u.Out, "  %s\n", a.ID)
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runRemove(args []string, u *ui.UI) error {
	var global, force bool
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.BoolVar(&global, "g", false, "")
	fs.BoolVar(&global, "global", false, "")
	fs.BoolVar(&force, "i", false, "")
	fs.BoolVar(&force, "install", false, "")
	fs.BoolVar(&force, "y", false, "")
	fs.BoolVar(&force, "yes", false, "")
	rest, err := parseFlags(fs, args)
	if err != nil {
		return usagef("%v", err)
	}
	if len(rest) != 1 {
		return usagef("remove takes one App ID (see 'dopt list')")
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
	return install.Remove(install.Env{Layout: l, User: usr, AsRoot: euid == 0, UI: u}, rest[0], force)
}
