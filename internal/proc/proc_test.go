package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Ominous-Josef/dopt/internal/sysuser"
)

// startFake runs a copy of sleep from inside dir, like an installed app.
func startFake(t *testing.T, dir string, argv0 string) *exec.Cmd {
	t.Helper()
	bin := filepath.Join(dir, "app")
	if _, err := os.Stat(bin); err != nil {
		data, err := os.ReadFile("/usr/bin/sleep")
		if err != nil {
			t.Skip("no /usr/bin/sleep")
		}
		os.WriteFile(bin, data, 0o755)
	}
	cmd := exec.Command(bin, "30")
	if argv0 != "" {
		cmd.Args[0] = argv0
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap it so it doesn't linger as a zombie.
	go cmd.Wait()
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd
}

func TestFindAndTerminate(t *testing.T) {
	inst := t.TempDir()
	cmd := startFake(t, inst, "")
	time.Sleep(50 * time.Millisecond)

	pids := FindPIDs(inst, "", os.Getuid())
	if !slices.Contains(pids, cmd.Process.Pid) {
		t.Fatalf("FindPIDs = %v, want %d", pids, cmd.Process.Pid)
	}
	if got := FindPIDs(inst, "", os.Getuid()+12345); len(got) != 0 {
		t.Errorf("other uid matched: %v", got)
	}
	if got := FindPIDs(t.TempDir(), "", os.Getuid()); slices.Contains(got, cmd.Process.Pid) {
		t.Error("process matched an unrelated folder")
	}

	Terminate(pids)
	if Alive(cmd.Process.Pid) {
		t.Error("process still alive after Terminate")
	}
}

func TestFindByCommandLink(t *testing.T) {
	inst := t.TempDir()
	runner := t.TempDir()
	link := "/fake/bin/app"
	cmd := startFake(t, runner, link)
	time.Sleep(50 * time.Millisecond)
	if pids := FindPIDs(inst, link, os.Getuid()); !slices.Contains(pids, cmd.Process.Pid) {
		t.Errorf("argv0 == command link not matched: %v", pids)
	}
}

func TestLaunchEnv(t *testing.T) {
	u := sysuser.User{Name: "alice", Home: "/home/alice", UID: 1000, GID: 1000}
	inherited := []string{"HOME=/root", "SUDO_COMMAND=x", "WAYLAND_DISPLAY=wayland-0", "LANG=en_GB.UTF-8"}

	root := strings.Join(launchEnv(u, true, inherited), "\n")
	for _, want := range []string{"HOME=/home/alice", "USER=alice", "XDG_RUNTIME_DIR=/run/user/1000", "WAYLAND_DISPLAY=wayland-0", "DISPLAY=:0", "LANG=en_GB.UTF-8", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"} {
		if !strings.Contains(root, want) {
			t.Errorf("root env missing %s:\n%s", want, root)
		}
	}
	if strings.Contains(root, "SUDO_COMMAND") || strings.Contains(root, "HOME=/root") {
		t.Errorf("root env leaks root's environment:\n%s", root)
	}

	user := strings.Join(launchEnv(u, false, []string{"HOME=/home/alice", "DISPLAY=:1", "FOO=bar"}), "\n")
	for _, want := range []string{"HOME=/home/alice", "DISPLAY=:1", "FOO=bar", "XDG_RUNTIME_DIR=/run/user/1000"} {
		if !strings.Contains(user, want) {
			t.Errorf("user env missing %s:\n%s", want, user)
		}
	}
	if strings.Count(user, "DISPLAY=") != 1 {
		t.Errorf("DISPLAY duplicated:\n%s", user)
	}
}

func TestLaunchDetached(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "app")
	os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	u, err := sysuser.Real()
	if err != nil {
		t.Skip(err)
	}
	if err := Launch(script, u, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("launched app never ran")
}
