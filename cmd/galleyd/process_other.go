//go:build !darwin && !linux

package main

import "os/exec"

func configureBackgroundProcess(cmd *exec.Cmd) {
}
