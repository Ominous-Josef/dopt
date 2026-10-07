// Package ui prints dopt's output and reads answers to its prompts.
//
// Prefixes keep their meaning without color: [n/N] step, [+] success, [!] warning,
// [-] error (stderr), [?] prompt (stderr). Details are indented under their step.
// Color is used only when the stream is a terminal and NO_COLOR is unset.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	reset  = "\x1b[0m"
)

// ErrNoInput means stdin ended before a prompt was answered.
var ErrNoInput = errors.New("no answer (input ended)")

// UI writes progress to Out, errors and prompts to Err, and reads answers from In.
type UI struct {
	Out, Err io.Writer
	in       *bufio.Reader

	colorOut, colorErr bool

	// Home is shown as ~ in paths.
	Home string

	step, total int
}

// New builds a UI. colorOut/colorErr enable ANSI color on each stream.
func New(in io.Reader, out, errw io.Writer, colorOut, colorErr bool) *UI {
	return &UI{Out: out, Err: errw, in: bufio.NewReader(in), colorOut: colorOut, colorErr: colorErr}
}

// Std is a UI on the process's stdin, stdout and stderr.
func Std() *UI {
	noColor := os.Getenv("NO_COLOR") != ""
	return New(os.Stdin, os.Stdout, os.Stderr, !noColor && IsTerminal(os.Stdout), !noColor && IsTerminal(os.Stderr))
}

// IsTerminal reports whether f is a character device (a terminal).
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (u *UI) paint(code string, toErr bool) (string, string) {
	if (toErr && u.colorErr) || (!toErr && u.colorOut) {
		return code, reset
	}
	return "", ""
}

// SetSteps sets N in the [n/N] step counter and restarts the count.
func (u *UI) SetSteps(total int) { u.total, u.step = total, 0 }

// Step starts the next numbered step.
func (u *UI) Step(format string, a ...any) {
	u.step++
	on, off := u.paint(bold, false)
	fmt.Fprintf(u.Out, "%s[%d/%d]%s %s\n", on, u.step, u.total, off, fmt.Sprintf(format, a...))
}

// Detail prints a dimmed, indented line under the current step.
func (u *UI) Detail(format string, a ...any) {
	on, off := u.paint(dim, false)
	fmt.Fprintf(u.Out, "      %s%s%s\n", on, fmt.Sprintf(format, a...), off)
}

// Line prints an indented line at normal brightness (summary fields, lists).
func (u *UI) Line(format string, a ...any) {
	fmt.Fprintf(u.Out, "      %s\n", fmt.Sprintf(format, a...))
}

// Heading prints a bold line.
func (u *UI) Heading(format string, a ...any) {
	on, off := u.paint(bold, false)
	fmt.Fprintf(u.Out, "%s%s%s\n", on, fmt.Sprintf(format, a...), off)
}

// Blank prints an empty line.
func (u *UI) Blank() { fmt.Fprintln(u.Out) }

// OK prints a [+] success line.
func (u *UI) OK(format string, a ...any) {
	on, off := u.paint(green, false)
	fmt.Fprintf(u.Out, "%s[+]%s %s\n", on, off, fmt.Sprintf(format, a...))
}

// Warn prints a [!] warning line.
func (u *UI) Warn(format string, a ...any) {
	on, off := u.paint(yellow, false)
	fmt.Fprintf(u.Out, "%s[!]%s %s\n", on, off, fmt.Sprintf(format, a...))
}

// Error prints a [-] error line to stderr.
func (u *UI) Error(format string, a ...any) {
	on, off := u.paint(red, true)
	fmt.Fprintf(u.Err, "%s[-]%s %s\n", on, off, fmt.Sprintf(format, a...))
}

// Hint prints an indented follow-up line to stderr, under an Error.
func (u *UI) Hint(format string, a ...any) {
	fmt.Fprintf(u.Err, "    %s\n", fmt.Sprintf(format, a...))
}

// Path shows paths under Home as ~/...
func (u *UI) Path(p string) string {
	if u.Home != "" && strings.HasPrefix(p, u.Home+"/") {
		return "~/" + strings.TrimPrefix(p, u.Home+"/")
	}
	return p
}

// Ask prints a [?] prompt and returns the answer line without its newline.
// It returns ErrNoInput if input ends before any answer.
func (u *UI) Ask(format string, a ...any) (string, error) {
	on, off := u.paint(bold, true)
	fmt.Fprintf(u.Err, "%s[?]%s %s", on, off, fmt.Sprintf(format, a...))
	line, err := u.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		fmt.Fprintln(u.Err)
		return "", ErrNoInput
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// AskDefault is Ask that returns def for an empty answer.
func (u *UI) AskDefault(def, format string, a ...any) (string, error) {
	ans, err := u.Ask(format, a...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(ans) == "" {
		return def, nil
	}
	return ans, nil
}

// Confirm asks a yes/no question; the prompt should already show [Y/n] or [y/N].
// With defYes, anything except an answer starting with "n" is yes.
// Without it, only "y" or "yes" is yes.
func (u *UI) Confirm(defYes bool, format string, a ...any) (bool, error) {
	ans, err := u.Ask(format, a...)
	if err != nil {
		return false, err
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	if defYes {
		return !strings.HasPrefix(ans, "n"), nil
	}
	return ans == "y" || ans == "yes", nil
}
