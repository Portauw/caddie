//go:build !windows

package platform

import (
	"cmp"
	"os"
	"os/exec"
)

// RunEditor opens file in the user's $EDITOR (defaulting to vim).
// sh -c preserves $EDITOR's word-splitting (e.g. "code --wait").
func RunEditor(file string) error {
	editor := cmp.Or(os.Getenv("EDITOR"), "vim")
	cmd := exec.Command("sh", "-c", editor+` "$0"`, file)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
