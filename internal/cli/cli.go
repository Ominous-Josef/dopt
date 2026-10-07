// Package cli parses dopt's command line and dispatches to subcommands.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Ominous-Josef/dopt/internal/install"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

// Version is the dopt release.
const Version = "3.0.0-dev"

// usageError is a command-line mistake; it is followed by a pointer to the help.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// InstallOptions are the flags of `dopt install`.
type InstallOptions struct {
	Manifest string
	AppID    string
	Symlink  string
	URL      string
	File     string
	SHA256   string
	Download bool
	Cleanup  bool
	Force    bool
	Global   bool
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Run executes dopt with args (without the program name) and returns the exit code.
func Run(args []string) int {
	u := ui.Std()
	return report(u, run(args, u))
}

// report prints err and returns the exit code for it.
func report(u *ui.UI, err error) int {
	if err == nil {
		return 0
	}
	var ue usageError
	var ie *install.Error
	switch {
	case errors.As(err, &ue):
		u.Error("%s", ue.msg)
		u.Hint("Run 'dopt help' for usage.")
		return 2
	case errors.As(err, &ie):
		if ie.Msg != "" {
			u.Error("%s", ie.Msg)
		}
		for _, h := range ie.Hints {
			u.Hint("  %s", h)
		}
		return ie.Code
	case errors.Is(err, ui.ErrNoInput):
		u.Error("Deployment aborted (no answer).")
		return 1
	}
	msg := err.Error()
	u.Error("%s", strings.ToUpper(msg[:1])+msg[1:]+".")
	return 1
}

func run(args []string, u *ui.UI) error {
	out := u.Out
	if len(args) == 0 {
		printHelp(out)
		return nil
	}
	cmd, rest := args[0], args[1:]
	// dopt 2.x had no subcommands: `dopt -m recipe.json -d` still means install.
	if strings.HasPrefix(cmd, "-") {
		switch cmd {
		case "-h", "--help":
			printHelp(out)
			return nil
		case "-v", "--version":
			fmt.Fprintf(out, "dopt %s\n", Version)
			return nil
		}
		cmd, rest = "install", args
	}
	switch cmd {
	case "install":
		opts, err := parseInstall(rest)
		if errors.Is(err, flag.ErrHelp) {
			printHelp(out)
			return nil
		}
		if err != nil {
			return err
		}
		return runInstall(opts, u)
	case "version":
		fmt.Fprintf(out, "dopt %s\n", Version)
		return nil
	case "help":
		printHelp(out)
		return nil
	}
	return usagef("unknown command %q", cmd)
}

// parseInstall reads `dopt install` flags. Short and long forms share one variable.
func parseInstall(args []string) (InstallOptions, error) {
	var o InstallOptions
	var removedPath string
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	str := func(p *string, short, long string) {
		fs.StringVar(p, short, "", "")
		fs.StringVar(p, long, "", "")
	}
	boolean := func(p *bool, short, long string) {
		fs.BoolVar(p, short, false, "")
		fs.BoolVar(p, long, false, "")
	}
	str(&o.Manifest, "m", "manifest")
	str(&o.AppID, "a", "app-id")
	str(&o.Symlink, "s", "symlink-as")
	str(&o.URL, "u", "url")
	str(&o.File, "f", "file")
	str(&removedPath, "p", "path")
	fs.StringVar(&o.SHA256, "sha256", "", "")
	boolean(&o.Download, "d", "download")
	boolean(&o.Cleanup, "c", "cleanup")
	boolean(&o.Force, "i", "install")
	boolean(&o.Global, "g", "global")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, err
		}
		return o, usagef("%v", err)
	}
	if fs.NArg() > 0 {
		return o, usagef("unexpected argument %q", fs.Arg(0))
	}
	if removedPath != "" {
		return o, usagef("-p was removed; pass the archive with -f <file>")
	}
	if o.URL != "" {
		o.Download = true
	}
	if o.File != "" && o.Download {
		return o, usagef("choose one source: -f <file>, or -d / -u <url>")
	}
	if o.SHA256 != "" {
		o.SHA256 = strings.ToLower(o.SHA256)
		if !sha256Pattern.MatchString(o.SHA256) {
			return o, usagef("--sha256 expects a 64-character hexadecimal SHA-256 checksum")
		}
	}
	return o, nil
}

func printHelp(w io.Writer) {
	fmt.Fprintf(w, `dopt %s - Directory Optional Package Manager
Installs and updates standalone Linux apps shipped as archives.

Usage:
  dopt install [options]      Install or update an app (same App ID = update)
  dopt version                Show the dopt version
  dopt help                   Show this help

Install options:
  -m, --manifest <json>   The application manifest (recipe)
  -a, --app-id <id>       App ID, when not using a manifest
  -s, --symlink-as <name> Command name to link (overrides the manifest's 'symlink_as')

  Source (choose one; if omitted, dopt offers your recent downloads):
  -d, --download          Download using the manifest's source
  -u, --url <url>         Download from this URL instead
  -f, --file <path>       Install from a local archive
      --sha256 <hash>     Verify the archive's SHA-256 checksum before installing

  -g, --global            Install system-wide to /opt (requires sudo)
  -c, --cleanup           Delete the downloaded or local archive after a successful setup
  -i, --install           Skip confirmation prompts; stop and relaunch a running app

Apps install into ~/.local/opt/<app_id> (or /opt/<app_id> with --global).
Set NO_COLOR to disable colored output.
`, Version)
}
