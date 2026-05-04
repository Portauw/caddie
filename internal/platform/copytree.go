package platform

import (
	"io"
	"os"
	"path/filepath"
)

// CopyTree copies src to dst recursively, preserving file mode bits.
// Symlinks in src are not followed — only regular files and directories are
// copied. dst must not exist; callers should os.RemoveAll(dst) first if
// they need a clean replacement.
func CopyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, info.Mode()&os.ModePerm); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		li, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		switch {
		case li.IsDir():
			if err := CopyTree(srcPath, dstPath); err != nil {
				return err
			}
		case li.Mode().IsRegular():
			if err := copyFile(srcPath, dstPath, li.Mode()&os.ModePerm); err != nil {
				return err
			}
		}
		// Symlinks are intentionally skipped: CopyTree is used for backups
		// where the real content has already been migrated.
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
