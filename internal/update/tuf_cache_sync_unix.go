//go:build !windows

package update

func syncTUFCacheDirectory(directory string) error {
	return syncDirectory(directory)
}
