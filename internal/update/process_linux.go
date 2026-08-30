//go:build linux

package update

import (
	"golang.org/x/sys/unix"
)

var atomicExchange exchangeFunction = func(left, right string) error {
	return unix.Renameat2(unix.AT_FDCWD, left, unix.AT_FDCWD, right, unix.RENAME_EXCHANGE)
}

var processExec execFunction = unix.Exec
