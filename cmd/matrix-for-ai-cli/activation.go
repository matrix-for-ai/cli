package main

import (
	"net"
	"os"
	"strconv"
)

func activatedListener() (net.Listener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, nil
	}
	if n, _ := strconv.Atoi(os.Getenv("LISTEN_FDS")); n < 1 {
		return nil, nil
	}
	os.Unsetenv("LISTEN_PID")
	os.Unsetenv("LISTEN_FDS")
	os.Unsetenv("LISTEN_FDNAMES")
	return net.FileListener(os.NewFile(3, "systemd-socket"))
}
