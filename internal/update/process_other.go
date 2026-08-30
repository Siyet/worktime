//go:build !linux

package update

import "fmt"

var atomicExchange exchangeFunction = func(string, string) error {
	return fmt.Errorf("atomic executable exchange is supported only on Linux")
}

var processExec execFunction = func(string, []string, []string) error {
	return fmt.Errorf("same-process executable handoff is supported only on Linux")
}
