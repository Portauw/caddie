//go:build windows

package platform

import (
	"cmp"
	"os"
	"os/exec"
	"strings"
)

// RunEditor opens file in the user's %EDITOR% (defaulting to notepad.exe).
// %EDITOR% may contain flags (e.g. "code --wait"), so the value is split on
// whitespace and the binary is exec'd directly without a shell.
func RunEditor(file string) error {
	editor := cmp.Or(os.Getenv("EDITOR"), "notepad.exe")
	parts := strings.Fields(editor)
	args := append(parts[1:], file)
	cmd := exec.Command(parts[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
