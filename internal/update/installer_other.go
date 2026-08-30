//go:build !linux

package update

import (
	"context"
	"fmt"
)

type unsupportedInstaller struct{ reason string }

func newNativeInstaller(runtime NativeRuntime) Installer {
	reason := "Self-update v1 supports standalone Linux amd64/arm64 only."
	if runtime.Packaging == "docker" {
		reason = "This Docker installation must be updated by pulling the manifest image digest and recreating the container."
	}
	return &unsupportedInstaller{reason: reason}
}

func (i *unsupportedInstaller) Supported() (bool, string) { return false, i.reason }
func (i *unsupportedInstaller) Apply(context.Context, Manifest, Asset) error {
	return fmt.Errorf("%s", i.reason)
}
