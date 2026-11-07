//go:build !windows

package worker

import "os/exec"

// quiet вне Windows ничего не меняет: консольных окон там нет.
func quiet(cmd *exec.Cmd) *exec.Cmd { return cmd }
