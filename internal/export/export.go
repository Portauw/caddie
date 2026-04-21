// Package export implements `ai-env export` — copying resolved skills into
// either a local directory or an S3 prefix. Mirrors the bash helpers
// _export_to_dir and _export_to_s3 byte-for-byte: the dry-run plan lines,
// the summary sentences, and the clean-before-copy ordering must stay intact
// so the contract tests pass.
//
// S3 shells out to the `aws` CLI (matching bash). There is no AWS SDK
// dependency by design — see the commit message on the export port.
package export

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ANSI codes duplicated from cmd/ai-env/main.go. Keeping a local copy avoids
// importing the main package and keeps this package self-contained; there
// are only ~6 codes and they're effectively a public contract.
const (
	ansiReset = "\033[0m"
	ansiDim   = "\033[2m"
	ansiBlue  = "\033[0;34m"
	ansiRed   = "\033[0;31m"
	ansiGreen = "\033[0;32m"
)

// Entry is one resolved skill to be exported: Name is the store dirname,
// RealDir is the filesystem directory whose contents are copied.
type Entry struct {
	Name    string
	RealDir string
}

// Options controls the two modes shared between dir and S3 exports.
type Options struct {
	Clean  bool
	DryRun bool
}

// countFiles counts regular files under dir (recursive). Matches
// `find "$real_dir" -type f | wc -l` in bash. Errors silently count 0,
// matching bash's `2>/dev/null`.
func countFiles(dir string) int {
	n := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

// copyDir copies src to dst recursively, preserving file mode bits. Symlinks
// in the source are preserved as symlinks (bash's `cp -a` behavior). dst is
// created if needed; existing dst contents are NOT merged — callers are
// expected to remove dst first (mirrors bash `rm -rf "$dest"` before `cp -a`).
func copyDir(src, dst string) error {
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
		// Use Lstat so we don't follow symlinks — bash's `cp -a` preserves them.
		li, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		switch {
		case li.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
		case li.IsDir():
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		default:
			if err := copyFile(srcPath, dstPath, li.Mode()&os.ModePerm); err != nil {
				return err
			}
		}
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

// Local implements _export_to_dir.
func Local(target string, entries []Entry, opts Options) error {
	matched := make(map[string]bool, len(entries))
	for _, e := range entries {
		matched[e.Name] = true
	}

	exported, removed := 0, 0

	if opts.Clean {
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			existing, _ := os.ReadDir(target)
			// bash iterates `$target/*/` (shell glob → lexicographic). Go's ReadDir
			// returns sorted entries already.
			names := make([]string, 0, len(existing))
			for _, e := range existing {
				full := filepath.Join(target, e.Name())
				fi, err := os.Stat(full)
				if err != nil || !fi.IsDir() {
					continue
				}
				names = append(names, e.Name())
			}
			sort.Strings(names)
			for _, name := range names {
				if matched[name] {
					continue
				}
				if opts.DryRun {
					fmt.Printf("  %sremove%s %s/\n", ansiRed, ansiReset, name)
				} else {
					if err := os.RemoveAll(filepath.Join(target, name)); err != nil {
						return err
					}
				}
				removed++
			}
		}
	}

	for _, e := range entries {
		dest := filepath.Join(target, e.Name)
		fileCount := countFiles(e.RealDir)
		if opts.DryRun {
			fmt.Printf("  %scopy%s   %s/  %s(%d files) ← %s%s\n",
				ansiGreen, ansiReset, e.Name, ansiDim, fileCount, e.RealDir, ansiReset)
		} else {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			if err := os.RemoveAll(dest); err != nil {
				return err
			}
			if err := copyDir(e.RealDir, dest); err != nil {
				return err
			}
		}
		exported++
	}

	if opts.DryRun {
		fmt.Println()
		fmt.Printf("%sℹ%s  Dry run: would export %d skills, remove %d\n",
			ansiBlue, ansiReset, exported, removed)
	} else {
		fmt.Printf("%s✓%s  Exported %d skills to %s/\n",
			ansiGreen, ansiReset, exported, target)
		if removed > 0 {
			fmt.Printf("%sℹ%s  Removed %d stale skills\n",
				ansiBlue, ansiReset, removed)
		}
	}
	return nil
}

// parseS3URI splits an s3://bucket/prefix URI into bucket and prefix
// (prefix has any trailing '/' stripped). Matches bash sed pipeline.
func parseS3URI(uri string) (bucket, prefix string) {
	rest := strings.TrimPrefix(uri, "s3://")
	if i := strings.Index(rest, "/"); i >= 0 {
		bucket = rest[:i]
		prefix = strings.TrimRight(rest[i+1:], "/")
	} else {
		bucket = rest
	}
	return
}

// awsFlags reproduces bash: pass through AWS_PROFILE / AWS_ENDPOINT_URL as
// CLI flags (not env — matches bash exactly for trace/debug parity).
func awsFlags() []string {
	var flags []string
	if p := os.Getenv("AWS_PROFILE"); p != "" {
		flags = append(flags, "--profile", p)
	}
	if ep := os.Getenv("AWS_ENDPOINT_URL"); ep != "" {
		flags = append(flags, "--endpoint-url", ep)
	}
	return flags
}

// awsListPrefixes runs `aws s3 ls s3://bucket/prefix/` and parses the `PRE`
// lines to get immediate subdirectories. Returns an empty slice on error,
// matching bash `|| true` leniency.
func awsListPrefixes(bucket, prefix string, flags []string) []string {
	args := append([]string{}, flags...)
	args = append(args, "s3", "ls", fmt.Sprintf("s3://%s/%s/", bucket, prefix))
	out, err := exec.Command("aws", args...).Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "PRE" {
			names = append(names, strings.TrimRight(fields[1], "/"))
		}
	}
	return names
}

// S3 implements _export_to_s3.
func S3(uri string, entries []Entry, opts Options) error {
	bucket, prefix := parseS3URI(uri)
	flags := awsFlags()

	matched := make(map[string]bool, len(entries))
	for _, e := range entries {
		matched[e.Name] = true
	}

	exported, removed := 0, 0

	if opts.Clean {
		existing := awsListPrefixes(bucket, prefix, flags)
		sort.Strings(existing)
		for _, name := range existing {
			if name == "" || matched[name] {
				continue
			}
			if opts.DryRun {
				fmt.Printf("  %sremove%s s3://%s/%s/%s/\n", ansiRed, ansiReset, bucket, prefix, name)
			} else {
				args := append([]string{}, flags...)
				args = append(args, "s3", "rm",
					fmt.Sprintf("s3://%s/%s/%s/", bucket, prefix, name),
					"--recursive")
				// Bash discards output; we do the same. Don't fail the whole
				// export if a single remove bombs — preserve bash leniency.
				_ = exec.Command("aws", args...).Run()
			}
			removed++
		}
	}

	for _, e := range entries {
		s3Dest := fmt.Sprintf("s3://%s/%s/%s/", bucket, prefix, e.Name)
		fileCount := countFiles(e.RealDir)
		if opts.DryRun {
			fmt.Printf("  %supload%s %s  %s(%d files) ← %s%s\n",
				ansiGreen, ansiReset, s3Dest, ansiDim, fileCount, e.RealDir, ansiReset)
		} else {
			args := append([]string{}, flags...)
			args = append(args, "s3", "cp", e.RealDir, s3Dest, "--recursive")
			_ = exec.Command("aws", args...).Run()
		}
		exported++
	}

	if opts.DryRun {
		fmt.Println()
		fmt.Printf("%sℹ%s  Dry run: would upload %d skills, remove %d\n",
			ansiBlue, ansiReset, exported, removed)
	} else {
		fmt.Printf("%s✓%s  Exported %d skills to s3://%s/%s/\n",
			ansiGreen, ansiReset, exported, bucket, prefix)
		if removed > 0 {
			fmt.Printf("%sℹ%s  Removed %d stale skills\n",
				ansiBlue, ansiReset, removed)
		}
	}
	return nil
}
