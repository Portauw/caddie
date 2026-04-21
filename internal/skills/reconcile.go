// Reconcile + fingerprint helpers for cmd_activate. Mirror the bash
// compute_fingerprint and reconcile_skill_dir functions byte-for-byte so
// projects do not re-run reconcile when nothing changed.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// ComputeFingerprint mirrors bash compute_fingerprint:
//
//	{ echo "$env_name"; cat; } | shasum -a 256 | cut -d' ' -f1
//
// where stdin is the sorted skill dirnames joined with newlines.
// Equivalent input bytes: "<env>\n<skill1>\n<skill2>\n...\n".
func ComputeFingerprint(envName string, sortedSkills []string) string {
	var b strings.Builder
	b.WriteString(envName)
	b.WriteByte('\n')
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
// expected set and minimally adds/removes. Entries that aren't symlinks are
// left alone (matches bash `[[ ! -L "$item" ]] && continue`). The skill store
// path is used as the symlink source (same layout as bash).
//
// Missing store entries are silently skipped — matches bash's `[[ -d ||
// -L $source_path ]]` guard.
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

	// Remove symlinks that aren't in want.
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

	// Add missing symlinks.
	for name := range want {
		tgt := filepath.Join(targetDir, name)
		if li, err := os.Lstat(tgt); err == nil && li.Mode()&os.ModeSymlink != 0 {
			continue
		}
		src := filepath.Join(store, name)
		info, err := os.Lstat(src)
		if err != nil {
			continue
		}
		_ = info
		if err := os.Symlink(src, tgt); err == nil {
			res.Added++
		}
	}
	return res, nil
}
