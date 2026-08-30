//go:build windows

package update

// Windows does not support fsync on a directory handle. The root file itself
// is still written, flushed, closed, and atomically renamed before this call.
func syncTUFCacheDirectory(string) error {
	return nil
}
