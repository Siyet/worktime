//go:build !linux

package update

import (
	"context"
	"os/exec"
	"time"
)

func runCommand(ctx context.Context, path string, arguments, environment []string, directory string) ([]byte, error) {
	preflightContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(preflightContext, path, arguments...)
	command.Env = environment
	command.Dir = directory
	return command.CombinedOutput()
}
