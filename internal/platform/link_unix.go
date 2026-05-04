//go:build !windows

package platform

import "os"

// Materialize creates a symlink at dst pointing to src.
func Materialize(src, dst string) (LinkKind, error) {
	if err := os.Symlink(src, dst); err != nil {
		return KindSymlink, err
	}
	return KindSymlink, nil
}

// ReadTarget returns the symlink target of dst.
func ReadTarget(dst string) (string, error) {
	return os.Readlink(dst)
}

// IsLinked reports whether fi describes a symlink.
func IsLinked(fi os.FileInfo, _ string) bool {
	return fi.Mode()&os.ModeSymlink != 0
}
