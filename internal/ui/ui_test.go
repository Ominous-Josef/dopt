package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newTest(input string, color bool) (*UI, *bytes.Buffer, *bytes.Buffer) {
	var out, errw bytes.Buffer
	return New(strings.NewReader(input), &out, &errw, color, color), &out, &errw
}

func TestPlainOutput(t *testing.T) {
	u, out, errw := newTest("", false)
	u.SetSteps(3)
	u.Step("Checking %s", "app")
	u.Detail("detail")
	u.OK("done")
	u.Warn("careful")
	u.Error("broken")
	want := "[1/3] Checking app\n      detail\n[+] done\n[!] careful\n"
	if out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
	if errw.String() != "[-] broken\n" {
		t.Errorf("err = %q", errw.String())
	}
	if strings.Contains(out.String()+errw.String(), "\x1b") {
		t.Error("plain output contains escape codes")
	}
}

func TestColorOutput(t *testing.T) {
	u, out, _ := newTest("", true)
	u.OK("done")
	if !strings.Contains(out.String(), "\x1b[32m[+]") {
		t.Errorf("out = %q, want green [+]", out.String())
	}
}

func TestAskAndConfirm(t *testing.T) {
	u, _, errw := newTest("My App\n\nno\nYes\n\nmaybe", false)
	if ans, _ := u.Ask("Name: "); ans != "My App" {
		t.Errorf("Ask kept %q, want spaces preserved", ans)
	}
	if ans, _ := u.AskDefault("def", "Name [def]: "); ans != "def" {
		t.Errorf("AskDefault = %q", ans)
	}
	if ok, _ := u.Confirm(true, "Go? [Y/n]: "); ok {
		t.Error(`"no" with default yes should be no`)
	}
	if ok, _ := u.Confirm(false, "Replace? [y/N]: "); !ok {
		t.Error(`"Yes" with default no should be yes`)
	}
	if ok, _ := u.Confirm(false, "Replace? [y/N]: "); ok {
		t.Error("empty with default no should be no")
	}
	// Last line without a trailing newline still counts as an answer.
	if ans, err := u.Ask("x: "); err != nil || ans != "maybe" {
		t.Errorf("Ask = %q, %v", ans, err)
	}
	if _, err := u.Ask("x: "); !errors.Is(err, ErrNoInput) {
		t.Errorf("Ask at EOF err = %v, want ErrNoInput", err)
	}
	if !strings.HasPrefix(errw.String(), "[?] Name: ") {
		t.Errorf("prompts go to stderr, got %q", errw.String())
	}
}

func TestPath(t *testing.T) {
	u, _, _ := newTest("", false)
	u.Home = "/home/a"
	if got := u.Path("/home/a/.local/opt/x"); got != "~/.local/opt/x" {
		t.Errorf("Path = %s", got)
	}
	if got := u.Path("/home/ab/x"); got != "/home/ab/x" {
		t.Errorf("Path = %s", got)
	}
}
