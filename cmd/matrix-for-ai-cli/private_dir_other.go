//go:build !windows

package main

import (
	"os"
	"syscall"
)

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return fail(errSystem, "%s is not a folder", dir)
	case st == nil || int(st.Uid) != os.Getuid():
		return fail(errSystem, "%s is not yours", dir)
	case fi.Mode().Perm() != 0o700:
		return fail(errSystem, "%s is not mode 700", dir)
	}
	return nil
}
