// Package platform provides OS-agnostic wrappers for filesystem operations
// that differ meaningfully between Unix and Windows. All platform-specific
// code lives in build-tagged *_unix.go / *_windows.go files; callers use
// only the functions declared there.
package platform

// LinkKind describes how Materialize created a managed link.
type LinkKind int

const (
	KindSymlink  LinkKind = iota // os.Symlink succeeded
	KindJunction                 // NTFS directory junction (Windows fallback)
)
