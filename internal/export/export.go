// Package export implements `caddie export` — copying resolved skills into
// either a local directory or an S3 prefix. The dry-run plan lines, summary
// sentences, and the clean-before-copy ordering are pinned by contract tests.
//
// S3 shells out to the `aws` CLI rather than depending on an AWS SDK.
package export

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Portauw/caddie/internal/skills"
)

// ANSI codes — duplicated from cmd/caddie/main.go to keep this package
// importable without pulling in main.
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

// selfContained drops any entry holding a symlink that resolves outside its
// own directory, and reports what it dropped.
//
// An export must not carry content from outside the skill it names. The S3
// backend shells out to `aws s3 cp --recursive`, which follows symlinks, so
// a skill containing `ref.md -> ~/.ssh/id_rsa` uploaded the key itself; the
// local backend recreates the link verbatim, which at best ships a path that
// means nothing on the machine the export is for. Skills placed in the store
// by hand never pass through WalkRepoSkills, so its containment checks have
// never applied to them — this is the only place they get checked.
func selfContained(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if skills.ContainsEscapingSymlink(e.RealDir) {
			fmt.Printf("  %sskip%s   %s/  %s(contains a symlink pointing outside the skill)%s\n",
				ansiRed, ansiReset, e.Name, ansiDim, ansiReset)
			continue
		}
		out = append(out, e)
	}
	return out
}

// Options controls the two modes shared between dir and S3 exports.
type Options struct {
	Clean  bool
	DryRun bool

	// ConfirmRemove is called before --clean deletes anything, with the
	// directory names about to be removed, and must return true to proceed.
	// --clean recursively deletes every subdirectory of the target that
	// isn't a matched skill, and the target is whatever the user passed to
	// --to — there is no marker file proving it is a caddie export
	// directory, so `--to ~/Documents` is a working command. A nil hook
	// means "never remove": callers have to opt in explicitly rather than
	// inherit a destructive default.
	ConfirmRemove func(names []string) bool
}

// countFiles counts regular files under dir (recursive). Errors are silently
// counted as zero.
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

// copyDir copies src to dst recursively, preserving file mode bits and
// symlinks (cp -a semantics). Callers must remove dst first if they want a
// clean copy — copyDir does not merge into existing destinations.
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
		// Lstat so source symlinks are preserved verbatim, not followed.
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

// Local copies entries into target/. With Clean, sibling directories not in
// entries are removed first.
func Local(target string, entries []Entry, opts Options) error {
	// matched is built from the full list, before filtering: a skill dropped
	// for containment must not then look "stale" to --clean and get an
	// already-exported copy deleted out from under it.
	matched := make(map[string]bool, len(entries))
	for _, e := range entries {
		matched[e.Name] = true
	}
	entries = selfContained(entries)

	exported, removed := 0, 0

	if opts.Clean {
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			existing, _ := os.ReadDir(target)
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
			stale := make([]string, 0, len(names))
			for _, name := range names {
				if !matched[name] {
					stale = append(stale, name)
				}
			}
			if opts.DryRun {
				for _, name := range stale {
					fmt.Printf("  %sremove%s %s/\n", ansiRed, ansiReset, name)
				}
				removed = len(stale)
			} else if len(stale) > 0 {
				if opts.ConfirmRemove == nil || !opts.ConfirmRemove(stale) {
					return fmt.Errorf("aborted: --clean would delete %d existing directories under %s", len(stale), target)
				}
				for _, name := range stale {
					if err := os.RemoveAll(filepath.Join(target, name)); err != nil {
						return err
					}
					removed++
				}
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
// (prefix has any trailing '/' stripped).
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

// awsFlags converts AWS_PROFILE / AWS_ENDPOINT_URL into explicit CLI flags
// so the exact command shows up in trace/debug output.
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
// lines to get immediate subdirectories. Returns nil on error.
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

// S3 uploads entries under s3://<bucket>/<prefix>/ via the aws CLI.
func S3(uri string, entries []Entry, opts Options) error {
	bucket, prefix := parseS3URI(uri)
	flags := awsFlags()

	// matched is built from the full list, before filtering: a skill dropped
	// for containment must not then look "stale" to --clean and get an
	// already-exported copy deleted out from under it.
	matched := make(map[string]bool, len(entries))
	for _, e := range entries {
		matched[e.Name] = true
	}
	entries = selfContained(entries)

	exported, removed := 0, 0

	if opts.Clean {
		existing := awsListPrefixes(bucket, prefix, flags)
		sort.Strings(existing)
		stale := make([]string, 0, len(existing))
		for _, name := range existing {
			if name != "" && !matched[name] {
				stale = append(stale, name)
			}
		}
		if opts.DryRun {
			for _, name := range stale {
				fmt.Printf("  %sremove%s s3://%s/%s/%s/\n", ansiRed, ansiReset, bucket, prefix, name)
			}
			removed = len(stale)
		} else if len(stale) > 0 {
			if opts.ConfirmRemove == nil || !opts.ConfirmRemove(stale) {
				return fmt.Errorf("aborted: --clean would delete %d existing prefixes under s3://%s/%s", len(stale), bucket, prefix)
			}
			for _, name := range stale {
				args := append([]string{}, flags...)
				args = append(args, "s3", "rm",
					fmt.Sprintf("s3://%s/%s/%s/", bucket, prefix, name),
					"--recursive")
				// A single rm failing should not abort the rest of the export.
				_ = exec.Command("aws", args...).Run()
				removed++
			}
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
