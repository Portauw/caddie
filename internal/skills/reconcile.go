// Reconcile + fingerprint helpers for cmd_activate.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// ComputeFingerprint returns the SHA-256 of the sorted skill list. The
// fingerprint file lives inside the profile folder, so the profile needs no
// separate identity in the hash.
func ComputeFingerprint(sortedSkills []string) string {
	var b strings.Builder
	for _, s := range sortedSkills {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// ReconcileResult reports the add/remove counts from ReconcileSkillDir.
type ReconcileResult struct {
	Added   int
	Removed int
}

// ReconcileSkillDir diffs the existing symlinks in targetDir against the
// expected set and minimally adds/removes/refreshes. Non-symlink entries are
// left alone; missing store entries are silently skipped. Symlinks that
// already exist but point to the wrong target (e.g. after a config-dir
// rename) are removed and re-created.
func ReconcileSkillDir(targetDir string, expected []string, store string) (ReconcileResult, error) {
	var res ReconcileResult
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return res, err
	}

	want := make(map[string]struct{}, len(expected))
	for _, e := range expected {
		if e == "" {
			continue
		}
		want[e] = struct{}{}
	}

	entries, err := os.ReadDir(targetDir)
	if err == nil {
		for _, e := range entries {
			full := filepath.Join(targetDir, e.Name())
			li, err := os.Lstat(full)
			if err != nil || li.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if _, ok := want[e.Name()]; !ok {
				if os.Remove(full) == nil {
					res.Removed++
				}
			}
		}
	}

	for name := range want {
		tgt := filepath.Join(targetDir, name)
		src := filepath.Join(store, name)
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		if li, err := os.Lstat(tgt); err == nil {
			// Already a symlink — only keep it if the target matches.
			if li.Mode()&os.ModeSymlink != 0 {
				if current, err := os.Readlink(tgt); err == nil && current == src {
					continue
				}
				_ = os.Remove(tgt)
			} else {
				// Non-symlink existing entry (real file/dir) — leave it alone.
				continue
			}
		}
		if err := os.Symlink(src, tgt); err == nil {
			res.Added++
		}
	}
	return res, nil
}
