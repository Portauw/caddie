//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

var junctionWarnOnce sync.Once

// Materialize creates a managed link at dst pointing to src.
// It tries os.Symlink first (succeeds when Developer Mode is enabled),
// then falls back to an NTFS directory junction via mklink /J.
func Materialize(src, dst string) (LinkKind, error) {
	if err := os.Symlink(src, dst); err == nil {
		return KindSymlink, nil
	}
	// Junction fallback. mklink /J requires an absolute target path.
	absSrc := src
	if !filepath.IsAbs(src) {
		absSrc = filepath.Clean(filepath.Join(filepath.Dir(dst), src))
	}
	cmdStr := fmt.Sprintf(`mklink /J "%s" "%s"`, dst, absSrc)
	out, err := exec.Command("cmd", "/c", cmdStr).CombinedOutput()
	if err != nil {
		return KindJunction, fmt.Errorf("junction failed (%s): %w", strings.TrimSpace(string(out)), err)
	}
	junctionWarnOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "ℹ  caddie: using NTFS junctions (enable Developer Mode in Windows Settings for real symlinks)")
	})
	return KindJunction, nil
}

// ReadTarget returns the link target of dst (symlink or junction).
// The \??\ prefix that Windows prepends to junction targets is stripped.
func ReadTarget(dst string) (string, error) {
	target, err := os.Readlink(dst)
	if err != nil {
		return "", err
	}
	target = strings.TrimPrefix(target, `\??\`)
	return filepath.Clean(target), nil
}

// isReparsePoint reports whether path has the Windows REPARSE_POINT file
// attribute, which covers both symlinks and NTFS directory junctions.
func isReparsePoint(path string) bool {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return false
	}
	return attrs&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// IsLinked reports whether fi or path is a caddie-managed link. On Windows,
// NTFS junctions appear as plain directories in os.Lstat — we detect them
// via the REPARSE_POINT file attribute.
func IsLinked(fi os.FileInfo, path string) bool {
	return fi.Mode()&os.ModeSymlink != 0 || isReparsePoint(path)
}
