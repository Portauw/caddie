package platform_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Portauw/caddie/internal/platform"
)

// TestMaterializeAndReadTarget verifies the round-trip: Materialize creates a
// link, ReadTarget returns the source, IsLinked reports true for the result.
func TestMaterializeAndReadTarget(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "link")

	kind, err := platform.Materialize(src, dst)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if runtime.GOOS == "windows" {
		// Either symlink (Dev Mode on) or junction is acceptable.
		if kind != platform.KindSymlink && kind != platform.KindJunction {
			t.Errorf("unexpected LinkKind %d", kind)
		}
	} else {
		if kind != platform.KindSymlink {
			t.Errorf("got kind %d, want KindSymlink", kind)
		}
	}

	target, err := platform.ReadTarget(dst)
	if err != nil {
		t.Fatalf("ReadTarget: %v", err)
	}
	// On Windows, ReadTarget normalises junction targets to absolute paths;
	// on Unix it returns exactly what was passed to os.Symlink.
	// Compare resolved paths to handle both cases.
	wantAbs, _ := filepath.Abs(src)
	gotAbs, _ := filepath.Abs(target)
	if gotAbs != wantAbs {
		t.Errorf("ReadTarget: got %q, want %q (abs: %q vs %q)", target, src, gotAbs, wantAbs)
	}

	li, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if !platform.IsLinked(li, dst) {
		t.Error("IsLinked returned false for a caddie-managed link")
	}
}

// TestIsLinkedPlainDir verifies that a plain directory is not reported as a link.
func TestIsLinkedPlainDir(t *testing.T) {
	dir := t.TempDir()
	li, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if platform.IsLinked(li, dir) {
		t.Error("IsLinked returned true for a plain directory")
	}
}

// TestFilesystemRoot verifies the root for the current OS.
func TestFilesystemRoot(t *testing.T) {
	root := platform.FilesystemRoot("/some/path")
	if runtime.GOOS == "windows" {
		// FilesystemRoot of a path without a volume returns `\`
		// (UNC paths aside, which caddie doesn't support).
		if root != `\` && len(root) != 3 { // e.g. "C:\"
			t.Errorf("unexpected root %q", root)
		}
	} else {
		if root != "/" {
			t.Errorf("got %q, want /", root)
		}
	}
}

// TestFilesystemRootWindowsVolume verifies that a Windows volume path is
// handled (skipped on non-Windows where the path is meaningless).
func TestFilesystemRootWindowsVolume(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only")
	}
	root := platform.FilesystemRoot(`C:\Users\foo\bar`)
	if root != `C:\` {
		t.Errorf("got %q, want C:\\", root)
	}
}

// TestCopyTree verifies that CopyTree copies files and directories recursively.
func TestCopyTree(t *testing.T) {
	src := t.TempDir()
	// Write a nested structure.
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := platform.CopyTree(src, dst); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}

	for _, rel := range []string{"a.txt", filepath.Join("sub", "b.txt")} {
		data, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("missing %s: %v", rel, err)
			continue
		}
		_ = data
	}
}
