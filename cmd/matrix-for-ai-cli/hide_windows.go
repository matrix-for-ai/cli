package main

import (
	"os/exec"
	"syscall"
)

func hide(cmd *exec.Cmd) {
	const createNoWindow, detachedProcess = 0x08000000, 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow | detachedProcess}
}
