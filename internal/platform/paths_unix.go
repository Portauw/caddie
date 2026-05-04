//go:build !windows

package platform

// FilesystemRoot returns "/" — the sole filesystem root on Unix.
func FilesystemRoot(_ string) string { return "/" }

// SameVolume always returns true on Unix (single root filesystem).
func SameVolume(_, _ string) bool { return true }
