package cli

import (
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/Ominous-Josef/dopt/internal/install"
	"github.com/Ominous-Josef/dopt/internal/ui"
)

func TestParseInstallAliases(t *testing.T) {
	short, err := parseInstall([]string{"-m", "r.json", "-a", "app", "-s", "cmd", "-f", "x.tar.gz", "-c", "-i", "-g"})
	if err != nil {
		t.Fatal(err)
	}
	long, err := parseInstall([]string{"--manifest", "r.json", "--app-id", "app", "--symlink-as", "cmd", "--file", "x.tar.gz", "--cleanup", "--install", "--global"})
	if err != nil {
		t.Fatal(err)
	}
	if short != long {
		t.Errorf("short %+v != long %+v", short, long)
	}
	if !short.Cleanup || !short.Force || !short.Global || short.Symlink != "cmd" {
		t.Errorf("parsed = %+v", short)
	}
}

func TestParseInstallURLImpliesDownload(t *testing.T) {
	o, err := parseInstall([]string{"-u", "https://example.com/a.tar.gz"})
	if err != nil || !o.Download {
		t.Errorf("-u: %+v, %v", o, err)
	}
}

func TestParseInstallSHA256(t *testing.T) {
	hash := strings.Repeat("AB", 32)
	o, err := parseInstall([]string{"--sha256", hash})
	if err != nil || o.SHA256 != strings.ToLower(hash) {
		t.Errorf("--sha256: %+v, %v", o, err)
	}
	if _, err := parseInstall([]string{"--sha256", "abc"}); err == nil {
		t.Error("short hash: want error")
	}
}

func TestParseInstallErrors(t *testing.T) {
	cases := map[string][]string{
		"-p was removed":      {"-p", "."},
		"choose one source":   {"-f", "a.tar.gz", "-d"},
		"unexpected argument": {"stray"},
		"not defined":         {"--bogus"},
	}
	for want, args := range cases {
		_, err := parseInstall(args)
		var ue usageError
		if !errors.As(err, &ue) || !strings.Contains(ue.msg, want) {
			t.Errorf("parseInstall(%v) = %v, want usage error mentioning %q", args, err, want)
		}
	}
	if _, err := parseInstall([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
}

func TestRunDispatch(t *testing.T) {
	var out, errb strings.Builder
	u := ui.New(strings.NewReader(""), &out, &errb, false, false)
	if err := run([]string{"version"}, u); err != nil || !strings.Contains(out.String(), Version) {
		t.Errorf("version: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := run([]string{"--version"}, u); err != nil || !strings.Contains(out.String(), Version) {
		t.Errorf("--version: %q, %v", out.String(), err)
	}
	var ue usageError
	if err := run([]string{"frobnicate"}, u); !errors.As(err, &ue) {
		t.Errorf("unknown command: %v", err)
	}
}

func TestReport(t *testing.T) {
	var out, errb strings.Builder
	u := ui.New(strings.NewReader(""), &out, &errb, false, false)
	if code := report(u, &install.Error{Msg: "Owned.", Hints: []string{"remove it"}, Code: 1}); code != 1 {
		t.Errorf("code = %d", code)
	}
	if code := report(u, &install.Error{Code: 0}); code != 0 {
		t.Errorf("stopped code = %d", code)
	}
	if code := report(u, usagef("bad flag")); code != 2 {
		t.Errorf("usage code = %d", code)
	}
	if code := report(u, errors.New("archive not found: x")); code != 1 {
		t.Errorf("plain code = %d", code)
	}
	want := "[-] Owned.\n      remove it\n[-] bad flag\n    Run 'dopt help' for usage.\n[-] Archive not found: x.\n"
	if errb.String() != want {
		t.Errorf("stderr = %q, want %q", errb.String(), want)
	}
}
