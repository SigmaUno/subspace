//go:build unix

package gateway

import (
	"os/exec"
	"syscall"
)

func setGroup(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
