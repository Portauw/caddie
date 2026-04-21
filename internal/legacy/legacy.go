// Package legacy embeds the frozen bash implementation of ai-env and
// executes it as a fallback for subcommands that haven't been ported to Go yet.
//
// The embedded script is a build-time copy of ./ai-env at the repo root.
// scripts/build.sh refreshes internal/legacy/ai-env-legacy.sh before `go build`.
package legacy

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Portauw/ai-env/internal/version"
)

//go:embed ai-env-legacy.sh
var script []byte

// Run extracts the embedded bash script to a stable cache path and runs it
// with the given args, returning bash's exit error. Callers that should not
// continue after delegation (the fallthrough in main) use Exec instead.
func Run(args []string) error {
	path, err := materialize()
	if err != nil {
		return fmt.Errorf("materialize legacy script: %w", err)
	}
	cmd := exec.Command("bash", append([]string{path}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}

// Exec runs the embedded bash and os.Exit's with its exit code. Only the
// process error (e.g. couldn't materialize) returns to the caller.
func Exec(args []string) error {
	if err := Run(args); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}

func materialize() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheDir, "ai-env")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "legacy-"+version.Version+".sh")

	if existing, err := os.ReadFile(path); err == nil && bytesEqual(existing, script) {
		return path, nil
	}
	if err := os.WriteFile(path, script, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
