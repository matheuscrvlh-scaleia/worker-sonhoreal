//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// Fora do Windows (desenvolvimento) não há job object.
type job struct{}

func novoJob() (job, error)            { return job{}, nil }
func (job) anexar(*os.Process) error   { return nil }
func configurarProcesso(cmd *exec.Cmd) {}
