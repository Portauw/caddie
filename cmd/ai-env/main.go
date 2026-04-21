// Command ai-env is the Go entry point for the ai-env tool.
//
// Strangler-pattern migration: subcommands listed in nativeCommands run the
// Go implementation; everything else falls through to the embedded frozen
// bash script. Each ported command must be byte-compatible with the bash
// output — see tests/contract.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Portauw/ai-env/internal/config"
	"github.com/Portauw/ai-env/internal/legacy"
	"github.com/Portauw/ai-env/internal/sources"
	"github.com/Portauw/ai-env/internal/version"
)

type handler func(args []string)

var nativeCommands = map[string]handler{
	"--version": cmdVersion,
	"-v":        cmdVersion,
	"which":     cmdWhich,
	"active":    cmdWhich,
	"list":      cmdList,
	"ls":        cmdList,
	"source":    cmdSource,
	"edit":      cmdEdit,
	"delete":    cmdDelete,
	"rm":        cmdDelete,
	"clone":     cmdClone,
	"cp":        cmdClone,
}

// ANSI codes mirroring the bash helpers so stdout stays byte-identical.
const (
	ansiBold   = "\033[1m"
	ansiReset  = "\033[0m"
	ansiCyan   = "\033[0;36m"
	ansiDim    = "\033[2m"
	ansiYellow = "\033[1;33m"
	ansiBlue   = "\033[0;34m"
	ansiRed    = "\033[0;31m"
	ansiGreen  = "\033[0;32m"
)

// isTerminal reports whether f refers to a tty (for prompt suppression,
// matching bash's `read -p` behavior when stdin is piped).
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func die(msg string) {
	fmt.Fprintf(os.Stderr, "%s✗%s  %s\n", ansiRed, ansiReset, msg)
	os.Exit(1)
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		if err := legacy.Exec(args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if fn, ok := nativeCommands[args[0]]; ok {
		fn(args[1:])
		return
	}

	if err := legacy.Exec(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func cmdVersion(_ []string) {
	fmt.Printf("ai-env v%s\n", version.Version)
}

func cmdList(_ []string) {
	envs := config.ListEnvs()
	if len(envs) == 0 {
		fmt.Printf("%s⚠%s  No environments found.\n", ansiYellow, ansiReset)
		fmt.Printf("%sℹ%s  Run %sai-env create <name>%s to get started.\n", ansiBlue, ansiReset, ansiCyan, ansiReset)
		return
	}

	fmt.Printf("%sEnvironments:%s\n\n", ansiBold, ansiReset)
	for _, name := range envs {
		file := config.EnvFile(name)
		displayName := config.ReadScalar(file, "name")
		description := config.ReadScalar(file, "description")
		skillCount := len(config.ReadList(file, "skills"))

		fmt.Printf("  %s%s%s%s\n", ansiBold, ansiCyan, name, ansiReset)
		if displayName != "" && displayName != name {
			fmt.Printf("    %s\n", displayName)
		}
		if description != "" {
			fmt.Printf("    %s%s%s\n", ansiDim, description, ansiReset)
		}
		if skillCount > 0 {
			fmt.Printf("    %s%d skill pattern(s)%s\n", ansiDim, skillCount, ansiReset)
		}
		fmt.Println()
	}

	fmt.Printf("  %sAuto-detect: %sai-env activate%s%s (resolves from cwd)%s\n\n",
		ansiDim, ansiCyan, ansiReset, ansiDim, ansiReset)
}

func cmdSource(args []string) {
	if len(args) == 0 {
		die("Usage: ai-env source <list|add|remove>")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		cmdSourceList(rest)
	case "add":
		cmdSourceAdd(rest)
	case "remove", "rm":
		cmdSourceRemove(rest)
	default:
		die("Unknown source command: " + sub)
	}
}

func cmdSourceList(_ []string) {
	path := filepath.Join(config.Dir(), "sources.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("%s⚠%s  No sources registered. Run %sai-env init%s or %sai-env source add%s.\n",
			ansiYellow, ansiReset, ansiCyan, ansiReset, ansiCyan, ansiReset)
		return
	}
	fmt.Printf("%sRegistered plugin sources:%s\n\n", ansiBold, ansiReset)
	fmt.Printf("  %s(~/.config/ai-env/skills/ is always scanned implicitly)%s\n\n", ansiDim, ansiReset)

	entries, err := sources.Parse()
	if err != nil {
		die(err.Error())
	}
	for _, e := range entries {
		name := e.Name
		if name == "" {
			name = "?"
		}
		mkt := e.Marketplace
		if mkt == "" {
			mkt = "?"
		}
		plg := e.Plugin
		if plg == "" {
			plg = "?"
		}
		fmt.Printf("  %-20s %s/%s\n", name, mkt, plg)
	}
}

func cmdSourceAdd(args []string) {
	if len(args) < 3 || args[0] == "" || args[1] == "" || args[2] == "" {
		die("Usage: ai-env source add <name> <marketplace> <plugin>\n  Example: ai-env source add superpowers superpowers-dev superpowers")
	}
	name, mkt, plg := args[0], args[1], args[2]

	cachePath := filepath.Join(sources.PluginCacheDir(), mkt, plg)
	if info, err := os.Stat(cachePath); err != nil || !info.IsDir() {
		die(fmt.Sprintf("Plugin cache not found: %s\n  Available marketplaces: %s", cachePath, sources.AvailableMarketplaces()))
	}

	exists, err := sources.ExistsStrict(name)
	if err != nil {
		die(err.Error())
	}
	if exists {
		die(fmt.Sprintf("Source '%s' already registered. Remove it first with: ai-env source remove %s", name, name))
	}

	if err := sources.Append(sources.Entry{Name: name, Marketplace: mkt, Plugin: plg}); err != nil {
		die(err.Error())
	}
	fmt.Printf("%s✓%s  Registered source: %s%s%s (%s/%s)\n", ansiGreen, ansiReset, ansiBold, name, ansiReset, mkt, plg)
}

func cmdSourceRemove(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: ai-env source remove <name>")
	}
	name := args[0]
	exists, err := sources.ExistsLoose(name)
	if err != nil {
		die(err.Error())
	}
	if !exists {
		die(fmt.Sprintf("Source '%s' not found.", name))
	}
	if err := sources.Remove(name); err != nil {
		die(err.Error())
	}
	fmt.Printf("%s✓%s  Removed source: %s%s%s\n", ansiGreen, ansiReset, ansiBold, name, ansiReset)
}

func cmdEdit(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: ai-env edit <name>")
	}
	name := args[0]
	if !config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' not found.", name))
	}
	file := config.EnvFile(name)

	// Bash: `$EDITOR "$file"` — unquoted expansion so EDITOR may contain args.
	// Use sh -c to preserve that word-splitting behavior.
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	cmd := exec.Command("sh", "-c", editor+` "$0"`, file)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Bash runs `$EDITOR "$file"` without checking exit status — we match.
	_ = cmd.Run()
	fmt.Printf("%s✓%s  Updated: %s\n", ansiGreen, ansiReset, name)
}

func cmdDelete(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: ai-env delete <name>")
	}
	name := args[0]
	if !config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' not found.", name))
	}

	// Bash: `read -rp "Are you sure..." -n 1 confirm; echo ""`
	// bash's `read -p` only writes the prompt when stdin is a terminal;
	// when stdin is piped the prompt is suppressed. Mirror that so piped
	// tests match byte-for-byte. Then read one byte and emit a newline.
	if isTerminal(os.Stdin) {
		fmt.Printf("Are you sure you want to delete '%s'? [y/N] ", name)
	}
	one := make([]byte, 1)
	n, _ := io.ReadFull(bufio.NewReader(os.Stdin), one)
	fmt.Println()
	confirm := ""
	if n == 1 {
		confirm = string(one)
	}
	if confirm != "y" && confirm != "Y" {
		return
	}

	if err := os.Remove(config.EnvFile(name)); err != nil {
		die(err.Error())
	}
	fmt.Printf("%s✓%s  Deleted: %s\n", ansiGreen, ansiReset, name)
}

func cmdClone(args []string) {
	if len(args) < 2 || args[0] == "" || args[1] == "" {
		die("Usage: ai-env clone <source> <destination>")
	}
	src, dest := args[0], args[1]
	if !config.EnvExists(src) {
		die(fmt.Sprintf("Source environment '%s' not found.", src))
	}
	if config.EnvExists(dest) {
		die(fmt.Sprintf("Destination environment '%s' already exists.", dest))
	}

	// Bash uses `cp` which preserves content byte-for-byte. Read+write matches.
	data, err := os.ReadFile(config.EnvFile(src))
	if err != nil {
		die(err.Error())
	}
	if err := os.WriteFile(config.EnvFile(dest), data, 0o644); err != nil {
		die(err.Error())
	}
	fmt.Printf("%s✓%s  Cloned: %s -> %s\n", ansiGreen, ansiReset, src, dest)
	fmt.Printf("%sℹ%s  Edit with: %sai-env edit %s%s\n", ansiBlue, ansiReset, ansiCyan, dest, ansiReset)
}

func cmdWhich(_ []string) {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Bash `cmd_which` captures resolve_env_from_cwd via $(), which swallows
	// the "unknown environment" warning from strategy 1. Match that here by
	// ignoring warnings. `activate` will surface them when it lands.
	name, _, ok := config.ResolveFromCwd(cwd)
	if !ok {
		fmt.Printf("%s⚠%s  No profile found for %s\n", ansiYellow, ansiReset, cwd)
		os.Exit(1)
	}
	fmt.Println(name)
}
