//go:build !unix

package hillclimb

import "os/exec"

func setGroup(*exec.Cmd) {}

func killGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return c.Process.Kill()
}
