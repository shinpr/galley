//go:build !darwin && !linux

package daemoncmd

import "os/exec"

func configureBackgroundProcess(cmd *exec.Cmd) {
}
