//go:build linux

package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const maxPreflightOutput = 64 << 10

type boundedOutput struct {
	mu        sync.Mutex
	data      []byte
	remaining int
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	written := len(data)
	if len(data) > output.remaining {
		output.data = append(output.data, data[:output.remaining]...)
		output.remaining = 0
		return written, fmt.Errorf("preflight output exceeded %d bytes", maxPreflightOutput)
	}
	output.data = append(output.data, data...)
	output.remaining -= len(data)
	return written, nil
}

func (output *boundedOutput) Bytes() []byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	return append([]byte(nil), output.data...)
}

func runCommand(ctx context.Context, path string, arguments, environment []string, directory string) ([]byte, error) {
	preflightContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.Command(path, arguments...)
	command.Env = environment
	command.Dir = directory
	// A staged release is verified but not trusted to observe runtime secrets or
	// contact the network before it has passed preflight. A fresh network namespace
	// makes the no-egress property kernel-enforced; inability to create it fails the
	// update closed rather than silently weakening preflight.
	systemAttributes := &syscall.SysProcAttr{Setpgid: true, Cloneflags: syscall.CLONE_NEWNET}
	if os.Geteuid() != 0 {
		systemAttributes.Cloneflags |= syscall.CLONE_NEWUSER
		systemAttributes.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
		systemAttributes.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
		systemAttributes.GidMappingsEnableSetgroups = false
	}
	command.SysProcAttr = systemAttributes
	output := &boundedOutput{remaining: maxPreflightOutput}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return output.Bytes(), err
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case err := <-waited:
		return output.Bytes(), err
	case <-preflightContext.Done():
		killErr := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		waitErr := <-waited
		return output.Bytes(), errors.Join(preflightContext.Err(), killErr, waitErr)
	}
}
