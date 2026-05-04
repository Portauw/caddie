//go:build windows

package platform

import (
	"path/filepath"
	"strings"
)

// FilesystemRoot returns the volume root for path p (e.g. "C:\").
// Used to terminate the walk-up loop in config.FindProjectConfig.
func FilesystemRoot(p string) string {
	v := filepath.VolumeName(p)
	if v == "" {
		return `\`
	}
	return v + `\`
}

// SameVolume reports whether paths a and b are on the same Windows volume.
func SameVolume(a, b string) bool {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b))
}
