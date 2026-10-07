// Package proc finds, stops and launches an app's processes.
package proc

import (
	"bytes"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ominous-Josef/dopt/internal/sysuser"
)

// procDir is /proc (replaced in tests).
var procDir = "/proc"

// FindPIDs lists processes of uid whose executable or argv[0] is inside
// installDir, or whose argv[0] is the app's command link.
func FindPIDs(installDir, binLink string, uid int) []int {
	if _, err := os.Stat(installDir); err != nil {
		return nil
	}
	real, err := filepath.EvalSymlinks(installDir)
	if err != nil {
		real = installDir
	}
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || processUID(pid) != uid {
			continue
		}
		exe, _ := os.Readlink(filepath.Join(procDir, e.Name(), "exe"))
		argv0 := ""
		if cmdline, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline")); err == nil {
			argv0, _, _ = strings.Cut(string(cmdline), "\x00")
		}
		if strings.HasPrefix(exe, real+"/") || strings.HasPrefix(argv0, real+"/") ||
			strings.HasPrefix(argv0, installDir+"/") || (binLink != "" && argv0 == binLink) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// processUID returns the effective uid of pid (as `pgrep -u` matches), or -1.
func processUID(pid int) int {
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "status"))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			if f := strings.Fields(rest); len(f) >= 2 {
				if uid, err := strconv.Atoi(f[1]); err == nil {
					return uid
				}
			}
		}
	}
	return -1
}

// Alive reports whether pid exists and isn't a zombie.
func Alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	// "pid (comm) S ...": the state follows the last ')'.
	if i := bytes.LastIndexByte(data, ')'); i >= 0 && i+2 < len(data) {
		return data[i+2] != 'Z'
	}
	return true
}

// Terminate asks pids to exit (SIGTERM), waits up to 3 seconds, then kills the rest.
func Terminate(pids []int) {
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	for i := 0; i < 30; i++ {
		alive := false
		for _, pid := range pids {
			if Alive(pid) {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// Launch starts binLink in the background as u, detached from dopt, with the
// user's graphical session environment. asRoot drops privileges to u first.
func Launch(binLink string, u sysuser.User, asRoot bool) error {
	cmd := exec.Command(binLink)
	cmd.Env = launchEnv(u, asRoot, os.Environ())
	cmd.Dir = u.Home
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if asRoot {
		cred := &syscall.Credential{Uid: uint32(u.UID), Gid: uint32(u.GID)}
		if acct, err := user.Lookup(u.Name); err == nil {
			if ids, err := acct.GroupIds(); err == nil {
				for _, id := range ids {
					if g, err := strconv.Atoi(id); err == nil {
						cred.Groups = append(cred.Groups, uint32(g))
					}
				}
			}
		}
		cmd.SysProcAttr.Credential = cred
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// launchEnv is the environment for a relaunched app. As root (under sudo) the
// inherited environment is root's, so a minimal user session environment is built.
func launchEnv(u sysuser.User, asRoot bool, inherited []string) []string {
	get := func(k string) string {
		for _, kv := range inherited {
			if v, ok := strings.CutPrefix(kv, k+"="); ok {
				return v
			}
		}
		return ""
	}
	runtimeDir := "/run/user/" + strconv.Itoa(u.UID)
	set := map[string]string{
		"DISPLAY":         get("DISPLAY"),
		"WAYLAND_DISPLAY": get("WAYLAND_DISPLAY"),
		"XDG_RUNTIME_DIR": runtimeDir,
	}
	if set["DISPLAY"] == "" {
		set["DISPLAY"] = ":0"
	}
	var env []string
	if asRoot {
		set["HOME"], set["USER"], set["LOGNAME"] = u.Home, u.Name, u.Name
		set["PATH"] = "/usr/local/bin:/usr/bin:/bin"
		set["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + runtimeDir + "/bus"
		for _, k := range []string{"LANG", "LC_ALL", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP"} {
			if v := get(k); v != "" {
				set[k] = v
			}
		}
	} else {
		for _, kv := range inherited {
			k, _, _ := strings.Cut(kv, "=")
			if _, override := set[k]; !override {
				env = append(env, kv)
			}
		}
	}
	for k, v := range set {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
