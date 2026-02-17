//go:build windows

package main

import "os/exec"

func setSysProcAttr(cmd *exec.Cmd) {
	// SysProcAttr.Setsid is not available on Windows; no-op on this platform
}
