// Package sysuser resolves the human user dopt acts for. Under sudo that is
// $SUDO_USER, not root, so homes, ownership and app relaunches use the right account.
package sysuser

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// User is the account dopt installs for.
type User struct {
	Name string
	Home string
	UID  int
	GID  int
}

// Real returns the invoking user: $SUDO_USER when running as root, otherwise the current user.
func Real() (User, error) {
	var (
		u   *user.User
		err error
	)
	if name := os.Getenv("SUDO_USER"); name != "" && os.Geteuid() == 0 {
		u, err = user.Lookup(name)
	} else {
		u, err = user.Current()
	}
	if err != nil {
		return User{}, fmt.Errorf("can't determine the real user: %w", err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return User{}, fmt.Errorf("unexpected uid %q for %s", u.Uid, u.Username)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return User{}, fmt.Errorf("unexpected gid %q for %s", u.Gid, u.Username)
	}
	if u.HomeDir == "" {
		return User{}, fmt.Errorf("user %s has no home directory", u.Username)
	}
	return User{Name: u.Username, Home: u.HomeDir, UID: uid, GID: gid}, nil
}
