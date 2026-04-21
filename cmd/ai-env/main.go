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
	"sort"
	"strings"
	"time"

	"github.com/Portauw/ai-env/internal/config"
	"github.com/Portauw/ai-env/internal/legacy"
	"github.com/Portauw/ai-env/internal/repos"
	"github.com/Portauw/ai-env/internal/skills"
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
	"repo":      cmdRepo,
	"inventory": cmdInventory,
	"help":      cmdHelp,
	"--help":    cmdHelp,
	"-h":        cmdHelp,
	"reset":     cmdReset,
	"show":      cmdShow,
	"info":      cmdShow,
	"create":    cmdCreate,
	"new":       cmdCreate,
	"init":      cmdInit,
	"scan":      cmdScan,
	"activate":  cmdActivate,
	"use":       cmdActivate,
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

// cmdSource rejects the removed source subcommand family. Plugin sources
// were dropped in favor of git repos as the only skill origin.
func cmdSource(_ []string) {
	die("`ai-env source` has been removed. Use `ai-env repo` to manage skill sources.")
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

// cmdRepo dispatches `repo <subcmd>`. Only list|ls is ported natively; every
// other subcommand falls through to the frozen bash script so writes stay
// funneled through the single existing implementation.
func cmdRepo(args []string) {
	if len(args) == 0 {
		die("Usage: ai-env repo <list|add|remove|update>")
	}
	sub := args[0]
	switch sub {
	case "list", "ls":
		cmdRepoList(args[1:])
	case "add":
		cmdRepoAdd(args[1:])
	case "remove", "rm":
		cmdRepoRemove(args[1:])
	case "update":
		cmdRepoUpdate(args[1:])
	default:
		die("Unknown repo command: " + sub)
	}
}

func cmdRepoList(_ []string) {
	entries, err := repos.Parse()
	if err != nil {
		die(err.Error())
	}
	if len(entries) == 0 {
		fmt.Printf("%s⚠%s  No git repos registered. Add one with: %sai-env repo add <name> <url>%s\n",
			ansiYellow, ansiReset, ansiCyan, ansiReset)
		return
	}
	fmt.Printf("%sRegistered git repos:%s\n\n", ansiBold, ansiReset)

	for _, e := range entries {
		repoDir := filepath.Join(repos.Dir(), e.Name)
		status := fmt.Sprintf("%s(not cloned)%s", ansiDim, ansiReset)
		if info, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil && info.IsDir() {
			branch := repos.GitOutput(repoDir, "rev-parse", "--abbrev-ref", "HEAD")
			if branch == "" {
				branch = "?"
			}
			shortHash := repos.GitOutput(repoDir, "rev-parse", "--short", "HEAD")
			if shortHash == "" {
				shortHash = "?"
			}
			status = fmt.Sprintf("%s%s@%s%s", ansiGreen, branch, shortHash, ansiReset)
		}
		fmt.Printf("  %s%s%s  %s\n", ansiBold, e.Name, ansiReset, status)
		fmt.Printf("    %s%s%s\n", ansiDim, e.URL, ansiReset)

		prefixDisplay := "none"
		if e.Prefix != "" && e.Prefix != "false" {
			if e.Prefix == "true" {
				prefixDisplay = e.Name + "-*"
			} else {
				prefixDisplay = e.Prefix + "-*"
			}
		}
		fmt.Printf("    %sskills_path: %s, prefix: %s%s\n", ansiDim, e.SkillsPath, prefixDisplay, ansiReset)
		fmt.Println("")
	}
}

// cmdRepoAdd mirrors bash cmd_repo_add: register a git repo under `repos:`
// in sources.yaml, then `git clone --depth 1` the URL into $CONFIG_DIR/repos/<name>.
// Clone failures print a warning (matching bash) but don't fail the command —
// the registration still succeeds so `ai-env repo update` can retry later.
func cmdRepoAdd(args []string) {
	if len(args) < 2 || args[0] == "" || args[1] == "" {
		die("Usage: ai-env repo add <name> <url> [skills_path]\n  Example: ai-env repo add lenny https://github.com/RefoundAI/lenny-skills skills")
	}
	name, url := args[0], args[1]
	skillsPath := "skills"
	if len(args) >= 3 && args[2] != "" {
		skillsPath = args[2]
	}

	// repos.Exists already returns false for a missing file or missing
	// `repos:` section, so no pre-check is needed.
	exists, err := repos.Exists(name)
	if err != nil {
		die(err.Error())
	}
	if exists {
		die(fmt.Sprintf("Repo '%s' already registered. Remove it first with: ai-env repo remove %s", name, name))
	}

	if err := repos.Append(repos.Entry{Name: name, URL: url, SkillsPath: skillsPath}); err != nil {
		die(err.Error())
	}

	if err := os.MkdirAll(repos.Dir(), 0o755); err != nil {
		die(err.Error())
	}
	repoDir := filepath.Join(repos.Dir(), name)
	// Bash only clones when $REPOS_DIR/$name doesn't exist — match that.
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		fmt.Printf("%sℹ%s  Cloning %s...\n", ansiBlue, ansiReset, url)
		clone := exec.Command("git", "clone", "--depth", "1", url, repoDir)
		// Stderr intentionally dropped to match bash's 2>/dev/null.
		if err := clone.Run(); err == nil {
			skillCount := 0
			skillsDir := filepath.Join(repoDir, skillsPath)
			if entries, err := os.ReadDir(skillsDir); err == nil {
				for _, e := range entries {
					if e.IsDir() {
						skillCount++
					}
				}
			}
			fmt.Printf("%s✓%s  Cloned: %s%s%s (%d skills found)\n", ansiGreen, ansiReset, ansiBold, name, ansiReset, skillCount)
		} else {
			fmt.Printf("%s⚠%s  Clone failed. Run %sai-env repo update %s%s to retry.\n", ansiYellow, ansiReset, ansiCyan, name, ansiReset)
		}
	}

	fmt.Printf("%s✓%s  Registered repo: %s%s%s\n", ansiGreen, ansiReset, ansiBold, name, ansiReset)
	fmt.Printf("%sℹ%s  Run %sai-env scan --force%s to sync skills into the store.\n", ansiBlue, ansiReset, ansiCyan, ansiReset)
}

// cmdRepoRemove mirrors bash cmd_repo_remove: unregister a repo from
// sources.yaml, remove any symlinks in the skill store pointing into its
// checkout, and rm -rf the checkout directory.
func cmdRepoRemove(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: ai-env repo remove <name>")
	}
	name := args[0]

	hasRepos, err := repos.HasReposSection()
	if err != nil {
		die(err.Error())
	}
	if !hasRepos {
		die("No repos section found in sources.yaml")
	}
	exists, err := repos.Exists(name)
	if err != nil {
		die(err.Error())
	}
	if !exists {
		die(fmt.Sprintf("Repo '%s' not found.", name))
	}

	if err := repos.Remove(name); err != nil {
		die(err.Error())
	}

	removed := 0
	store := skills.Store()
	repoDir := filepath.Join(repos.Dir(), name)
	needle := string(os.PathSeparator) + "repos" + string(os.PathSeparator) + name + string(os.PathSeparator)
	if entries, err := os.ReadDir(store); err == nil {
		for _, e := range entries {
			full := filepath.Join(store, e.Name())
			info, err := os.Lstat(full)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(full)
			if err != nil {
				continue
			}
			if strings.Contains(target, needle) {
				if err := os.Remove(full); err == nil {
					removed++
				}
			}
		}
	}

	if info, err := os.Stat(repoDir); err == nil && info.IsDir() {
		if err := os.RemoveAll(repoDir); err != nil {
			die(err.Error())
		}
		fmt.Printf("%sℹ%s  Removed cloned repo: %s\n", ansiBlue, ansiReset, repoDir)
	}

	fmt.Printf("%s✓%s  Removed repo: %s%s%s (%d skill symlinks cleaned)\n", ansiGreen, ansiReset, ansiBold, name, ansiReset, removed)
}

func cmdInventory(args []string) {
	filter := ""
	if len(args) > 0 {
		filter = args[0]
	}
	if !skills.StoreExists() {
		die(fmt.Sprintf("Canonical store not found. Run %sai-env init%s first.", ansiCyan, ansiReset))
	}

	fmt.Printf("%sSkill Inventory%s (%s%s%s)\n\n", ansiBold, ansiReset, ansiDim, skills.Store(), ansiReset)

	items, err := skills.Scan()
	if err != nil {
		die(err.Error())
	}

	// Group by prefix, preserving store sort order for item lists (python
	// iterates sorted(store.iterdir()) and appends in order).
	groups := map[string][]skills.Item{}
	for _, it := range items {
		groups[it.Prefix] = append(groups[it.Prefix], it)
	}
	prefixes := make([]string, 0, len(groups))
	for k := range groups {
		prefixes = append(prefixes, k)
	}
	sort.Strings(prefixes)

	for _, prefix := range prefixes {
		g := groups[prefix]
		fmt.Printf("  %s%s%s (%d skills)\n", ansiBold, prefix, ansiReset, len(g))
		for _, it := range g {
			skillID := fmt.Sprintf("%s:%s", prefix, it.Remainder)
			if filter != "" && !strings.Contains(skillID, filter) {
				continue
			}
			fmt.Printf("    %s\n", skillID)
		}
		fmt.Println()
	}

	fmt.Printf("Total: %d skills\n", len(items))
}

// cmdHelp mirrors bash usage(): a static multi-section help blob with ANSI
// color codes. Registered under help, --help, -h. Output must be byte-identical
// to `printf '%b\n' "$(cat << EOF ... EOF)"` in bash — note the trailing
// newline from printf's own \n and that backslash-escaped $ becomes $.
func cmdHelp(_ []string) {
	B, R, C, D, V := ansiBold, ansiReset, ansiCyan, ansiDim, version.Version
	fmt.Print(
		B + "ai-env" + R + " v" + V + " — Skill Profile Manager for AI Coding Agents\n" +
			"\n" +
			B + "USAGE" + R + "\n" +
			"  ai-env <command> [arguments] [flags]\n" +
			"\n" +
			B + "SETUP" + R + "\n" +
			"  " + C + "init" + R + "                        Initialize: migrate skills, first scan\n" +
			"\n" +
			B + "GIT REPOS" + R + "\n" +
			"  " + C + "repo" + R + " list                    Show registered git repos + status\n" +
			"  " + C + "repo" + R + " add <name> <url> [path] Register + clone a git skill repo\n" +
			"  " + C + "repo" + R + " remove <name>           Unregister repo + clean up symlinks\n" +
			"  " + C + "repo" + R + " update [name]           Pull latest changes (all or specific)\n" +
			"\n" +
			B + "DISCOVERY" + R + "\n" +
			"  " + C + "scan" + R + "    [-v]                 Scan repos, sync to skill store\n" +
			"  " + C + "inventory" + R + "                    List all skills with prefix grouping\n" +
			"\n" +
			B + "ENVIRONMENTS" + R + "\n" +
			"  " + C + "create" + R + "  <name>               Create a new environment interactively\n" +
			"  " + C + "list" + R + "    (ls)                 List all environments\n" +
			"  " + C + "show" + R + "    <name>               Show config + resolved skills\n" +
			"  " + C + "edit" + R + "    <name>               Open environment config in $EDITOR\n" +
			"  " + C + "activate" + R + " [name]              Resolve skills for cwd (or explicit env)\n" +
			"  " + C + "clone" + R + "   <src> <dest>         Clone an environment config\n" +
			"  " + C + "delete" + R + "  <name>               Delete an environment\n" +
			"  " + C + "which" + R + "                        Show currently active environment\n" +
			"  " + C + "reset" + R + "   [-f]                 Remove symlinks, store & re-enable plugins\n" +
			"\n" +
			B + "EXPORT" + R + "\n" +
			"  " + C + "export" + R + "  <env> --to <dir>       Copy resolved skills to local directory\n" +
			"  " + C + "export" + R + "  <env> --to s3://b/p/   Upload resolved skills to S3\n" +
			"  " + C + "export" + R + "  --all --to <target>    Export all skills (no env filter)\n" +
			"  Flags: --clean (remove stale), --dry-run (preview)\n" +
			"  Env:   AWS_PROFILE, AWS_ENDPOINT_URL (for S3 targets)\n" +
			"\n" +
			B + "FLAGS" + R + "\n" +
			"  -n, --dry-run             Show what would happen without executing\n" +
			"  -v, --verbose             Show detailed output\n" +
			"  -h, --help                Show this help message\n" +
			"\n" +
			B + "SKILL PATTERNS" + R + "\n" +
			"  Patterns use prefix:name format with glob wildcards:\n" +
			"    \"lenny:*\"          all skills from the lenny repo\n" +
			"    \"lenny:ai-*\"       repo skills matching ai-* (e.g. ai-evals)\n" +
			"    \"local:*\"          all hand-written skills (no prefix dash)\n" +
			"    \"*\"                everything\n" +
			"\n" +
			B + "EXAMPLES" + R + "\n" +
			"  ai-env init                              # First-time setup\n" +
			"  ai-env create my-project                 # Create environment\n" +
			"  ai-env activate                          # Auto-detect profile from cwd\n" +
			"  ai-env activate my-project               # Explicit profile activation\n" +
			"  ai-env activate --dry-run                # Preview what would change\n" +
			"  ai-env repo add lenny https://github.com/RefoundAI/lenny-skills  # Add git repo\n" +
			"  ai-env inventory                         # See all available skills\n" +
			"\n" +
			B + "SHELL INTEGRATION" + R + "\n" +
			"  Add to ~/.zshrc (or ~/.bashrc):\n" +
			"\n" +
			"    claude() {\n" +
			"      ai-env activate && command claude \"$@\"\n" +
			"    }\n" +
			"\n" +
			B + "PROJECT CONFIG" + R + "\n" +
			"  Create .ai-env.yaml in your project root:\n" +
			"\n" +
			"    environment: \"my-project\"\n" +
			"\n" +
			B + "DIRECTORIES" + R + "\n" +
			"  " + D + "~/.config/ai-env/skills/" + R + "            Skill store (source of truth)\n" +
			"  " + D + "~/.config/ai-env/repos/" + R + "             Cloned git repos\n" +
			"  " + D + "~/.config/ai-env/config.yaml" + R + "        Global settings (default_environment)\n" +
			"  " + D + "<project>/.agents/skills/" + R + "            Project skills (managed symlinks)\n" +
			"  " + D + "<project>/.claude/skills" + R + "             Symlink to .agents/skills\n" +
			"  " + D + "<project>/.ai-env.yaml" + R + "              Project profile binding\n" +
			"  " + D + "~/.config/ai-env/environments/" + R + "       Environment YAML files\n" +
			"  " + D + "~/.config/ai-env/sources.yaml" + R + "        Repo registry\n",
	)
	// Bash `$(cat << EOF)` strips trailing newlines from the heredoc body;
	// `printf '%b\n'` then adds one. Net tail is a single "\n" — matched above.
}

// cmdRepoUpdate mirrors bash cmd_repo_update: git pull each registered repo
// (or one filtered by name). Dies if no repos section present; reports
// "not found" if target name didn't match; clones on first run.
func cmdRepoUpdate(args []string) {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}

	// Bash: die if sources.yaml missing OR no `repos:` header.
	hasRepos, err := repos.HasReposSection()
	if err != nil {
		die(err.Error())
	}
	if !hasRepos {
		die("No git repos registered.")
	}

	entries, err := repos.Parse()
	if err != nil {
		die(err.Error())
	}

	updated := 0
	for _, e := range entries {
		if target != "" && e.Name != target {
			continue
		}
		repoDir := filepath.Join(repos.Dir(), e.Name)
		gitDir := filepath.Join(repoDir, ".git")

		if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
			fmt.Printf("%sℹ%s  Repo '%s' not cloned yet. Cloning...\n", ansiBlue, ansiReset, e.Name)
			if err := os.MkdirAll(repos.Dir(), 0o755); err != nil {
				die(err.Error())
			}
			clone := exec.Command("git", "clone", "--depth", "1", e.URL, repoDir)
			if clone.Run() == nil {
				fmt.Printf("%s✓%s  Cloned: %s\n", ansiGreen, ansiReset, e.Name)
				updated++
			} else {
				fmt.Printf("%s⚠%s  Failed to clone: %s\n", ansiYellow, ansiReset, e.Name)
			}
			continue
		}

		beforeHash := repos.GitOutput(repoDir, "rev-parse", "HEAD")

		fmt.Printf("%sℹ%s  Updating '%s'...\n", ansiBlue, ansiReset, e.Name)
		pull := exec.Command("git", "-C", repoDir, "pull", "--ff-only")
		// Bash: `git -C pull --ff-only 2>/dev/null` — stderr dropped; stdout
		// also goes to /dev/null (the bash form only checks exit status).
		if pull.Run() == nil {
			afterHash := repos.GitOutput(repoDir, "rev-parse", "HEAD")
			if beforeHash != afterHash {
				commitCount := repos.GitOutput(repoDir, "rev-list", beforeHash+".."+afterHash, "--count")
				if commitCount == "" {
					commitCount = "?"
				}
				fmt.Printf("%s✓%s  Updated: %s%s%s (%s new commit(s))\n",
					ansiGreen, ansiReset, ansiBold, e.Name, ansiReset, commitCount)
				updated++
			} else {
				fmt.Printf("  %s%s: already up to date%s\n", ansiDim, e.Name, ansiReset)
			}
		} else {
			fmt.Printf("%s⚠%s  Update failed for '%s'. Try: cd %s && git pull\n",
				ansiYellow, ansiReset, e.Name, repoDir)
		}
	}

	if target != "" && updated == 0 {
		found := false
		for _, e := range entries {
			if e.Name == target {
				found = true
				break
			}
		}
		if !found {
			die(fmt.Sprintf("Repo '%s' not found.", target))
		}
	}

	if updated > 0 {
		fmt.Println()
		fmt.Printf("%sℹ%s  Run %sai-env scan --force%s to sync updated skills into the store.\n",
			ansiBlue, ansiReset, ansiCyan, ansiReset)
	}
}

// cmdReset mirrors bash cmd_reset: clean managed symlinks in ~/.agents/skills,
// remove ~/.claude/skills symlink, clean project-local skill symlinks for every
// registered environment, rm -rf the skill store, remove state files, and
// re-enable disabled plugins registered via ai-env sources.
//
// Skipped vs bash: the plugin-enable step uses python to rewrite ~/.claude/
// settings.json — we delegate by reimplementing the narrow behavior (scan
// store symlinks, rewrite settings.json) only when settings.json exists.
func cmdReset(args []string) {
	force := false
	if len(args) > 0 && (args[0] == "-f" || args[0] == "--force") {
		force = true
	}

	fmt.Printf("%sai-env reset%s — restore to clean state\n\n", ansiBold, ansiReset)

	home := os.Getenv("HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = h
	}
	agentSkills := filepath.Join(home, ".agents", "skills")
	claudeSkills := filepath.Join(home, ".claude", "skills")
	skillStore := filepath.Join(config.Dir(), "skills")

	symlinkCountAgents := 0
	storeCount := 0
	claudeIsSymlink := false
	if info, err := os.Lstat(claudeSkills); err == nil && info.Mode()&os.ModeSymlink != 0 {
		claudeIsSymlink = true
	}
	if info, err := os.Stat(agentSkills); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(agentSkills); err == nil {
			for _, e := range entries {
				full := filepath.Join(agentSkills, e.Name())
				if li, err := os.Lstat(full); err == nil && li.Mode()&os.ModeSymlink != 0 {
					symlinkCountAgents++
				}
			}
		}
	}
	if info, err := os.Stat(skillStore); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(skillStore); err == nil {
			storeCount = len(entries)
		}
	}

	fmt.Printf("%sWill remove:%s\n", ansiBold, ansiReset)
	fmt.Printf("  %d managed symlink(s) in ~/.agents/skills/\n", symlinkCountAgents)
	if claudeIsSymlink {
		fmt.Printf("  ~/.claude/skills/ → ~/.agents/skills/ (symlink)\n")
	}
	fmt.Printf("  %d item(s) in skill store (~/.config/ai-env/skills/)\n", storeCount)
	fmt.Printf("  State files (.last-scan)\n")
	fmt.Printf("  Project-local skill symlinks (for all environments with directory: set)\n")
	fmt.Println()
	fmt.Printf("%sWill keep:%s\n", ansiBold, ansiReset)
	fmt.Printf("  Environment configs (~/.config/ai-env/environments/)\n")
	fmt.Printf("  Repo registry (~/.config/ai-env/sources.yaml)\n")
	fmt.Println()

	// Bash uses `echo -n "Proceed?..."; read -r confirm` here — an echo, not
	// `read -rp`, so the prompt fires regardless of tty state. Don't gate on
	// isTerminal like cmdDelete does.
	if !force {
		fmt.Print("Proceed? [y/N] ")
		br := bufio.NewReader(os.Stdin)
		line, _ := br.ReadString('\n')
		confirm := strings.TrimRight(line, "\n")
		if confirm != "y" && confirm != "Y" {
			fmt.Println("Aborted.")
			return
		}
		fmt.Println()
	}

	if info, err := os.Stat(agentSkills); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(agentSkills); err == nil {
			for _, e := range entries {
				full := filepath.Join(agentSkills, e.Name())
				if li, err := os.Lstat(full); err == nil && li.Mode()&os.ModeSymlink != 0 {
					_ = os.Remove(full)
				}
			}
		}
		fmt.Printf("%sℹ%s  Cleaned ~/.agents/skills/ symlinks\n", ansiBlue, ansiReset)
	}
	// Remove ~/.claude/skills if it's a symlink.
	if li, err := os.Lstat(claudeSkills); err == nil && li.Mode()&os.ModeSymlink != 0 {
		if os.Remove(claudeSkills) == nil {
			fmt.Printf("%sℹ%s  Removed ~/.claude/skills symlink\n", ansiBlue, ansiReset)
		}
	}

	// 5b. Clean project-local skill dirs for every environment with `directory:`.
	projectCleaned := 0
	envDir := config.EnvDir()
	if entries, err := os.ReadDir(envDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			envYAML := filepath.Join(envDir, e.Name())
			ed := config.ReadScalar(envYAML, "directory")
			if ed == "" {
				continue
			}
			if strings.HasPrefix(ed, "~") {
				ed = home + strings.TrimPrefix(ed, "~")
			}
			pdir := filepath.Join(ed, ".agents", "skills")
			if info, err := os.Stat(pdir); err == nil && info.IsDir() {
				if items, err := os.ReadDir(pdir); err == nil {
					for _, it := range items {
						full := filepath.Join(pdir, it.Name())
						if li, err := os.Lstat(full); err == nil && li.Mode()&os.ModeSymlink != 0 {
							if os.Remove(full) == nil {
								projectCleaned++
							}
						}
					}
				}
			}
			// Remove .claude/skills symlink + fingerprint.
			claudeLink := filepath.Join(ed, ".claude", "skills")
			if li, err := os.Lstat(claudeLink); err == nil && li.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(claudeLink)
			}
			_ = os.Remove(filepath.Join(ed, ".claude", ".ai-env-fingerprint"))
		}
	}
	if projectCleaned > 0 {
		fmt.Printf("%sℹ%s  Cleaned %d project-local symlink(s)\n", ansiBlue, ansiReset, projectCleaned)
	}

	if info, err := os.Stat(skillStore); err == nil && info.IsDir() {
		_ = os.RemoveAll(skillStore)
		fmt.Printf("%sℹ%s  Removed skill store\n", ansiBlue, ansiReset)
	}

	_ = os.Remove(filepath.Join(config.Dir(), ".last-scan"))
	fmt.Printf("%sℹ%s  Removed state files\n", ansiBlue, ansiReset)

	fmt.Println()
	fmt.Printf("%s✓%s  Reset complete. To rebuild, run: %sai-env scan && ai-env activate <env>%s\n",
		ansiGreen, ansiReset, ansiCyan, ansiReset)
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

// cmdShow mirrors bash cmd_show: print config file contents + resolved skills
// derived from the store via resolve_skills. Byte-exact stdout vs bash.
func cmdShow(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: ai-env show <name>")
	}
	name := args[0]
	if !config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' not found.", name))
	}
	file := config.EnvFile(name)
	displayName := config.ReadScalar(file, "name")
	directory := config.ReadScalar(file, "directory")

	label := displayName
	if label == "" {
		label = name
	}
	fmt.Printf("%sEnvironment: %s%s%s\n\n", ansiBold, ansiCyan, label, ansiReset)

	if directory != "" {
		// Bash `${directory/#\~/$HOME}` expands a leading `~` only.
		expanded := directory
		if strings.HasPrefix(expanded, "~") {
			home := os.Getenv("HOME")
			if home == "" {
				h, _ := os.UserHomeDir()
				home = h
			}
			expanded = home + strings.TrimPrefix(expanded, "~")
		}
		fmt.Printf("  %sSkills managed in:%s\n", ansiDim, ansiReset)
		fmt.Printf("    %s%s/.agents/skills/ (.claude/skills → symlink)%s\n", ansiDim, expanded, ansiReset)
		if _, err := os.Stat(filepath.Join(expanded, ".claude", ".ai-env-fingerprint")); err == nil {
			fmt.Printf("    %s(fingerprint: active)%s\n", ansiDim, ansiReset)
		}
		fmt.Println()
	}

	// Dump config verbatim — bash `cat "$file"`.
	data, err := os.ReadFile(file)
	if err != nil {
		die(err.Error())
	}
	os.Stdout.Write(data)

	fmt.Println()
	fmt.Printf("%sResolved skills:%s\n", ansiBold, ansiReset)

	patterns := config.ReadList(file, "skills")
	// If the store doesn't exist (init not run), resolve_skills silently
	// yields empty — match that instead of erroring out.
	var ids []string
	if skills.StoreExists() {
		ids, _ = skills.ResolveIDs(patterns)
	}
	// Bash quirk: `count=$(echo "$resolved" | grep -c . || echo 0)`. When
	// resolved is empty, grep -c . prints "0" and returns 1, so `|| echo 0`
	// fires too — $count captures "0\n0". We mirror that exactly.
	if len(ids) == 0 {
		fmt.Printf("  0\n0 skills matched\n\n")
	} else {
		fmt.Printf("  %d skills matched\n\n", len(ids))
	}
	for _, id := range ids {
		fmt.Printf("  %s\n", id)
	}
}

// cmdCreate mirrors bash cmd_create: interactive prompts for display name,
// description, directory, skill patterns; writes an environment YAML.
// Prompt text gated on tty for piped-stdin contract tests (same pattern as
// cmdDelete); bash `read -rp` natively suppresses the prompt when stdin is
// not a tty.
func cmdCreate(args []string) {
	if len(args) == 0 || args[0] == "" {
		die("Usage: ai-env create <name>")
	}
	name := args[0]
	if config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' already exists. Use 'ai-env edit %s' to modify it.", name, name))
	}
	file := config.EnvFile(name)

	fmt.Printf("%sCreating environment: %s%s%s\n\n", ansiBold, ansiCyan, name, ansiReset)

	tty := isTerminal(os.Stdin)
	br := bufio.NewReader(os.Stdin)
	readLine := func(prompt string) string {
		if tty {
			fmt.Print(prompt)
		}
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			return ""
		}
		return strings.TrimRight(line, "\n")
	}

	displayName := readLine(fmt.Sprintf("Display name [%s]: ", name))
	if displayName == "" {
		displayName = name
	}
	description := readLine("Description: ")
	directory := readLine(fmt.Sprintf("Working directory [~/Dev/%s]: ", name))
	if directory == "" {
		directory = "~/Dev/" + name
	}

	fmt.Println()
	fmt.Println("Skill patterns (enter one per line, empty line to finish):")
	fmt.Println(`  Examples: "gws:*", "superpowers:*", "local:*", "*" (all)`)
	var patterns []string
	for {
		p := readLine("  - ")
		if p == "" {
			break
		}
		patterns = append(patterns, p)
	}
	if len(patterns) == 0 {
		patterns = []string{"*"}
		fmt.Printf("%sℹ%s  Defaulting to all skills (\"*\")\n", ansiBlue, ansiReset)
	}

	var body strings.Builder
	fmt.Fprintf(&body, "name: \"%s\"\n", displayName)
	fmt.Fprintf(&body, "description: \"%s\"\n", description)
	fmt.Fprintf(&body, "directory: \"%s\"\n", directory)
	body.WriteString("\n")
	body.WriteString("skills:\n")
	for _, p := range patterns {
		fmt.Fprintf(&body, "  - \"%s\"\n", p)
	}
	body.WriteString("\n")
	body.WriteString("# agents:\n")
	body.WriteString("#   claude:\n")
	body.WriteString("#     model: \"claude-sonnet-4-6\"\n")
	body.WriteString("#     permission_mode: \"plan\"\n")
	body.WriteString("#     system_prompt_file: \"./CLAUDE.md\"\n")

	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		die(err.Error())
	}
	if err := os.WriteFile(file, []byte(body.String()), 0o644); err != nil {
		die(err.Error())
	}

	fmt.Println()
	fmt.Printf("%s✓%s  Created environment: %s%s%s\n", ansiGreen, ansiReset, ansiBold, name, ansiReset)
	fmt.Printf("%sℹ%s  Config: %s%s%s\n", ansiBlue, ansiReset, ansiDim, file, ansiReset)
	fmt.Printf("  Edit:     %sai-env edit %s%s\n", ansiCyan, name, ansiReset)
	fmt.Printf("  Preview:  %sai-env activate %s --dry-run%s\n", ansiCyan, name, ansiReset)
	fmt.Printf("  Activate: %sai-env activate %s%s\n", ansiCyan, name, ansiReset)
}

// cmdInit mirrors bash cmd_init: idempotent filesystem setup (config dirs,
// skill store, backups, migrations, ~/.claude/skills symlink, auto-detected
// plugin sources, example env). Delegates the final `cmd_scan` step to the
// legacy bash script because scan hasn't been ported yet.
func cmdInit(args []string) {
	fmt.Printf("%sInitializing ai-env skill profile manager...%s\n\n", ansiBold, ansiReset)

	home := os.Getenv("HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = h
	}
	configDir := config.Dir()
	envDir := config.EnvDir()
	skillStore := skills.Store()
	claudeSkills := filepath.Join(home, ".claude", "skills")
	agentSkills := filepath.Join(home, ".agents", "skills")
	sourcesFile := filepath.Join(configDir, "sources.yaml")

	_ = os.MkdirAll(configDir, 0o755)
	_ = os.MkdirAll(envDir, 0o755)
	_ = os.MkdirAll(skillStore, 0o755)
	fmt.Printf("%s✓%s  Config directory: %s\n", ansiGreen, ansiReset, configDir)
	fmt.Printf("%s✓%s  Environments: %s\n", ansiGreen, ansiReset, envDir)
	fmt.Printf("%s✓%s  Skill store: %s\n", ansiGreen, ansiReset, skillStore)

	date := time.Now().Format("2006-01-02")
	for _, d := range []string{claudeSkills, agentSkills} {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil || len(entries) == 0 {
			continue
		}
		backup := d + ".bak." + date
		if _, err := os.Stat(backup); err == nil {
			fmt.Printf("%sℹ%s  Backup already exists: %s\n", ansiBlue, ansiReset, backup)
			continue
		}
		// `cp -a` preserves attrs; use an external cp to keep semantics identical.
		cmd := exec.Command("cp", "-a", d, backup)
		if cmd.Run() == nil {
			fmt.Printf("%s✓%s  Backed up %s to %s\n", ansiGreen, ansiReset, d, backup)
		}
	}

	migrated := 0
	for _, src := range []string{claudeSkills, agentSkills} {
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			continue
		}
		for _, e := range entries {
			full := filepath.Join(src, e.Name())
			li, err := os.Lstat(full)
			if err != nil {
				continue
			}
			if li.Mode()&os.ModeSymlink != 0 {
				continue // skip symlinks (already managed)
			}
			base := e.Name()
			if !li.IsDir() && strings.HasSuffix(base, ".md") {
				// Bare .md — wrap into <name>/SKILL.md.
				skillName := strings.TrimSuffix(base, ".md")
				skillDir := filepath.Join(skillStore, skillName)
				if _, err := os.Stat(skillDir); err == nil {
					continue
				}
				if err := os.MkdirAll(skillDir, 0o755); err != nil {
					continue
				}
				data, err := os.ReadFile(full)
				if err != nil {
					continue
				}
				if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), data, 0o644); err != nil {
					continue
				}
				_ = os.Remove(full)
				fmt.Printf("%sℹ%s  Migrated file: %s -> %s/SKILL.md (from %s)\n", ansiBlue, ansiReset, base, skillName, src)
				migrated++
			} else if li.IsDir() {
				target := filepath.Join(skillStore, base)
				if _, err := os.Stat(target); err == nil {
					continue
				}
				if err := os.Rename(full, target); err != nil {
					continue
				}
				fmt.Printf("%sℹ%s  Migrated dir: %s (from %s)\n", ansiBlue, ansiReset, base, src)
				migrated++
			}
		}
	}
	if migrated > 0 {
		fmt.Printf("%s✓%s  Migrated %d items to skill store\n", ansiGreen, ansiReset, migrated)
	} else {
		fmt.Printf("%sℹ%s  No bare files/dirs to migrate\n", ansiBlue, ansiReset)
	}

	// 3b. Ensure ~/.claude/skills is a symlink to ~/.agents/skills.
	ensureClaudeSkillsSymlink(claudeSkills, agentSkills)
	fmt.Printf("%s✓%s  Set up ~/.claude/skills → ~/.agents/skills\n", ansiGreen, ansiReset)

	if _, err := os.Stat(sourcesFile); os.IsNotExist(err) {
		if err := os.WriteFile(sourcesFile, []byte("sources:\n"), 0o644); err != nil {
			die(err.Error())
		}
		fmt.Printf("%sℹ%s  Add repos with: %sai-env repo add <name> <url>%s\n",
			ansiBlue, ansiReset, ansiCyan, ansiReset)
	} else {
		fmt.Printf("%sℹ%s  Sources file already exists: %s\n", ansiBlue, ansiReset, sourcesFile)
	}

	if entries, err := os.ReadDir(envDir); err == nil && len(entries) == 0 {
		example := filepath.Join(envDir, "example.yaml")
		exampleBody := `name: "Example"
description: "An example environment -- edit or delete me"
directory: "~/projects/example"

skills:
  - "*"

# agents:
#   claude:
#     model: "claude-sonnet-4-6"
#     permission_mode: "plan"
`
		_ = os.WriteFile(example, []byte(exampleBody), 0o644)
		fmt.Printf("%sℹ%s  Created example environment: %sai-env show example%s\n",
			ansiBlue, ansiReset, ansiCyan, ansiReset)
	}

	// Run the native scan so init stays self-contained. Failures are non-fatal.
	fmt.Println()
	cmdScan(nil)

	fmt.Println()
	fmt.Printf("%sℹ%s  Next steps:\n", ansiBlue, ansiReset)
	fmt.Printf("  %sai-env repo list%s             Review registered repos\n", ansiCyan, ansiReset)
	fmt.Printf("  %sai-env inventory%s             See all discovered skills\n", ansiCyan, ansiReset)
	fmt.Printf("  %sai-env create my-project%s     Create your first environment\n", ansiCyan, ansiReset)
	fmt.Println()
	fmt.Printf("%sℹ%s  Shell integration (add to ~/.zshrc):\n", ansiBlue, ansiReset)
	fmt.Println()
	fmt.Printf("  %sclaude() {\n", ansiDim)
	fmt.Printf("    ai-env activate && command claude \"\\$@\"\n")
	fmt.Printf("  }%s\n", ansiReset)
}

// ensureClaudeSkillsSymlink mirrors bash ensure_claude_skills_symlink for the
// home case: if ~/.claude/skills is already the right symlink, no-op;
// otherwise remove (migrating contents into ~/.agents/skills if it was a
// real directory) and recreate as a relative symlink to "../.agents/skills".
func ensureClaudeSkillsSymlink(claudeDir, agentsDir string) {
	if li, err := os.Lstat(claudeDir); err == nil && li.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(claudeDir)
		if target == "../.agents/skills" {
			return
		}
		// Check whether it resolves to agentsDir.
		resolved, err1 := filepath.EvalSymlinks(claudeDir)
		absAgents, err2 := filepath.EvalSymlinks(agentsDir)
		if err1 == nil && err2 == nil && resolved == absAgents {
			return
		}
		_ = os.Remove(claudeDir)
	}
	if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
		_ = os.MkdirAll(agentsDir, 0o755)
		if entries, err := os.ReadDir(claudeDir); err == nil {
			for _, e := range entries {
				src := filepath.Join(claudeDir, e.Name())
				dst := filepath.Join(agentsDir, e.Name())
				li, err := os.Lstat(src)
				if err != nil {
					continue
				}
				if li.Mode()&os.ModeSymlink == 0 && li.IsDir() {
					if _, err := os.Stat(dst); os.IsNotExist(err) {
						_ = os.Rename(src, dst)
					}
				} else if li.Mode()&os.ModeSymlink != 0 {
					if _, err := os.Lstat(dst); os.IsNotExist(err) {
						target, _ := os.Readlink(src)
						_ = os.Symlink(target, dst)
					}
				}
			}
		}
		_ = os.RemoveAll(claudeDir)
	}
	_ = os.MkdirAll(filepath.Dir(claudeDir), 0o755)
	_ = os.Symlink("../.agents/skills", claudeDir)
}

// cmdScan mirrors bash cmd_scan but repo-only: plugin sources were dropped.
// Sweeps ~/.agents/skills/ into the store, cleans broken store symlinks,
// iterates every registered repo (checking for remote updates + syncing
// skills), then prints counts + warnings. Cached by mtime of .last-scan
// (60 s) unless --force.
func cmdScan(args []string) {
	verbose, force := false, false
	for _, a := range args {
		switch a {
		case "-v", "--verbose":
			verbose = true
		case "-f", "--force":
			force = true
		}
	}

	store := skills.Store()
	_ = os.MkdirAll(store, 0o755)

	cachePath := filepath.Join(config.Dir(), ".last-scan")
	if !force {
		if info, err := os.Stat(cachePath); err == nil {
			age := time.Since(info.ModTime())
			if age < 60*time.Second {
				if verbose {
					fmt.Printf("%sℹ%s  Scan cached (%ds ago, use --force to rescan)\n",
						ansiBlue, ansiReset, int(age.Seconds()))
				}
				return
			}
		}
	}

	fmt.Printf("%sℹ%s  Scanning skill sources...\n", ansiBlue, ansiReset)

	home := os.Getenv("HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = h
	}

	verbosef := func(format string, a ...any) { fmt.Printf(format, a...) }

	swept := skills.SweepAgentDir(home, verbose, verbosef)
	if swept > 0 {
		fmt.Printf("%sℹ%s  Swept %d new skill(s) from agent dirs into store\n", ansiBlue, ansiReset, swept)
	}

	brokenRemoved := skills.CleanBrokenStoreLinks(verbose, verbosef)
	if brokenRemoved > 0 {
		fmt.Printf("%sℹ%s  Removed %d broken symlink(s) from store\n", ansiBlue, ansiReset, brokenRemoved)
	}

	totalRepo, updates, shadowed, perRepo := skills.SyncRepos(verbose, verbosef)
	for _, r := range perRepo {
		if r.Warning != "" {
			switch r.Warning {
			case "not cloned":
				fmt.Printf("%s⚠%s  Repo '%s': not cloned. Run %sai-env repo update %s%s\n",
					ansiYellow, ansiReset, r.Name, ansiCyan, r.Name, ansiReset)
			case "skills_path not found":
				fmt.Printf("%s⚠%s  Repo '%s': skills_path '%s' not found\n",
					ansiYellow, ansiReset, r.Name, r.SkillsPath)
			}
			continue
		}
		fmt.Printf("%sℹ%s  Repo '%s': %d skills from %s/\n",
			ansiBlue, ansiReset, r.Name, r.Count, r.SkillsPath)
	}

	localCount, total := skills.CountStore()
	fmt.Println()
	now := time.Now()
	if err := os.Chtimes(cachePath, now, now); err != nil {
		_ = os.WriteFile(cachePath, nil, 0o644)
	}

	fmt.Printf("%s✓%s  Scan complete: %s%d%s skills in canonical store (%d local, %d from repos)\n",
		ansiGreen, ansiReset, ansiBold, total, ansiReset, localCount, totalRepo)

	if len(updates) > 0 {
		fmt.Println()
		fmt.Printf("%sℹ%s  Repo updates available:\n", ansiBlue, ansiReset)
		for _, u := range updates {
			fmt.Printf("  %s•%s %s%s%s: %s commit(s) behind\n",
				ansiYellow, ansiReset, ansiBold, u.Name, ansiReset, u.Behind)
		}
		fmt.Printf("  %sRun %sai-env repo update%s%s to pull changes%s\n",
			ansiDim, ansiCyan, ansiReset, ansiDim, ansiReset)
	}

	if len(shadowed) > 0 {
		fmt.Println()
		fmt.Printf("%s⚠%s  Local skills shadowing repo versions:\n", ansiYellow, ansiReset)
		for _, s := range shadowed {
			fmt.Printf("  %s•%s %s%s%s shadows repo '%s'\n",
				ansiYellow, ansiReset, ansiBold, s.SkillName, ansiReset, s.RepoName)
		}
		fmt.Printf("  %sTo use the repo version, remove the local copy:%s\n", ansiDim, ansiReset)
		fmt.Printf("  %s  rm -rf %s/<skill-name> && ai-env scan --force%s\n", ansiDim, store, ansiReset)
	}

	// Orphaned skill pattern warnings — scan every environment's `skills:`
	// list for patterns that match zero store items.
	envDir := config.EnvDir()
	envEntries, err := os.ReadDir(envDir)
	if err != nil {
		return
	}
	hasWarn := false
	for _, ef := range envEntries {
		if ef.IsDir() || !strings.HasSuffix(ef.Name(), ".yaml") {
			continue
		}
		envName := strings.TrimSuffix(ef.Name(), ".yaml")
		patterns := config.ReadList(filepath.Join(envDir, ef.Name()), "skills")
		for _, p := range patterns {
			if p == "" {
				continue
			}
			if skills.PatternMatches(p) == 0 {
				if !hasWarn {
					fmt.Println()
					hasWarn = true
				}
				fmt.Printf("%s⚠%s  Environment %s%s%s: pattern %s\"%s\"%s matches 0 skills — source may have been removed\n",
					ansiYellow, ansiReset, ansiBold, envName, ansiReset, ansiCyan, p, ansiReset)
			}
		}
	}
}

// expandTilde replaces a leading `~` with $HOME (bash `${var/#\~/$HOME}`).
func expandTilde(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home := os.Getenv("HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = h
	}
	return home + strings.TrimPrefix(p, "~")
}

// cmdActivate mirrors bash cmd_activate. Resolves the target environment
// (explicit arg, .ai-env.yaml, interactive picker), syncs repos, resolves
// skills, fingerprints, and minimally reconciles the target .agents/skills
// directory (+ .claude/skills symlink). Writes a fingerprint + .gitignore
// entries for project-local targets.
//
// Deliberate divergence from bash: no SOURCES.md manifest is generated. The
// .gitignore entry list keeps ".agents/SOURCES.md" so legacy files are still
// tracked as ignored when present, but we do not create one here.
func cmdActivate(args []string) {
	name := ""
	dryRun := false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		default:
			if strings.HasPrefix(a, "-") {
				die("Unknown flag: " + a)
			}
			name = a
		}
	}

	cwd, _ := os.Getwd()

	// Always probe the project config so we can use it as a fallback for
	// project_dir even when an explicit name is given.
	projectConfig := config.FindProjectConfig(cwd)

	if name == "" {
		if projectConfig != "" {
			if ref := config.ReadScalar(projectConfig, "environment"); ref != "" {
				if config.EnvExists(ref) {
					name = ref
				} else {
					fmt.Printf("%s⚠%s  .ai-env.yaml references unknown environment: %s\n",
						ansiYellow, ansiReset, ref)
				}
			}
		}
		if name == "" {
			// Fall back to the directory: matching rule.
			if n, _, ok := config.ResolveFromCwd(cwd); ok {
				name = n
			}
		}
		if name == "" {
			envs := config.ListEnvs()
			if len(envs) == 0 {
				die(fmt.Sprintf("No profiles found. Run %sai-env create <name>%s first.", ansiCyan, ansiReset))
			}
			base := filepath.Base(cwd)
			fmt.Printf("%sNo profile for %s%s\n\n", ansiBold, base, ansiReset)
			fmt.Printf("Pick an environment to use here:\n\n")
			for i, en := range envs {
				desc := config.ReadScalar(config.EnvFile(en), "description")
				fmt.Printf("  %s%d%s) %s%-20s%s %s%s%s\n",
					ansiCyan, i+1, ansiReset, ansiBold, en, ansiReset, ansiDim, desc, ansiReset)
			}
			fmt.Println()
			if !isTerminal(os.Stdin) {
				die("Invalid choice.")
			}
			fmt.Printf("  Choice [1-%d]: ", len(envs))
			br := bufio.NewReader(os.Stdin)
			line, _ := br.ReadString('\n')
			line = strings.TrimRight(line, "\n")
			idx := 0
			_, err := fmt.Sscanf(line, "%d", &idx)
			if err != nil || idx < 1 || idx > len(envs) {
				die("Invalid choice.")
			}
			name = envs[idx-1]
			// Remember the choice.
			projectConfig = filepath.Join(cwd, ".ai-env.yaml")
			_ = os.WriteFile(projectConfig, []byte(fmt.Sprintf("environment: \"%s\"\n", name)), 0o644)
			fmt.Printf("%s✓%s  Created .ai-env.yaml → %s\n\n", ansiGreen, ansiReset, name)
		}
	}

	if !config.EnvExists(name) {
		die(fmt.Sprintf("Environment '%s' not found. Run 'ai-env list' to see available environments.", name))
	}

	file := config.EnvFile(name)
	displayName := config.ReadScalar(file, "name")
	directory := config.ReadScalar(file, "directory")
	if directory != "" {
		directory = expandTilde(directory)
	}

	projectDir := ""
	if directory != "" {
		projectDir = directory
	} else if projectConfig != "" {
		projectDir = filepath.Dir(projectConfig)
	}

	home := os.Getenv("HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = h
	}

	var targetAgents, fingerprintDir string
	if projectDir != "" {
		targetAgents = filepath.Join(projectDir, ".agents", "skills")
		fingerprintDir = filepath.Join(projectDir, ".claude")
	} else {
		targetAgents = filepath.Join(home, ".agents", "skills")
		fingerprintDir = config.Dir()
	}
	fingerprintFile := filepath.Join(fingerprintDir, ".ai-env-fingerprint")

	label := displayName
	if label == "" {
		label = name
	}
	projLabel := projectDir
	if projLabel == "" {
		projLabel = "global"
	}
	fmt.Printf("%s%s%s %s→ %s%s\n", ansiBold, label, ansiReset, ansiDim, projLabel, ansiReset)

	// Repo sync step. Bash shells out to `cmd_scan` under various conditions;
	// we just invoke the native cmdScan which handles its own mtime cache.
	activateSyncRepos()

	patterns := config.ReadList(file, "skills")

	// Resolve matched skills + per-prefix counts.
	matched, prefixSummary := resolveMatchedWithSummary(patterns)
	skillCount := len(matched)

	if dryRun {
		fmt.Println()
		fmt.Printf("%sDry run — would activate:%s\n\n", ansiYellow, ansiReset)
		fmt.Printf("  Environment: %s%s%s\n", ansiBold, label, ansiReset)
		fmt.Printf("  Project dir: %s\n", projLabel)
		fmt.Printf("  Skills:      %d (%s)\n", skillCount, prefixSummary)
		fmt.Println()
		fmt.Printf("  %sWould manage:%s\n", ansiDim, ansiReset)
		fmt.Printf("    %s%s/ (.claude/skills → symlink)%s\n", ansiDim, targetAgents, ansiReset)
		fmt.Println()
		fmt.Printf("  Matched skills:\n")
		for _, s := range matched {
			fmt.Printf("    %s\n", s)
		}
		return
	}

	newFP := skills.ComputeFingerprint(name, matched)
	if data, err := os.ReadFile(fingerprintFile); err == nil {
		old := strings.TrimRight(string(data), "\n")
		if old == newFP {
			fmt.Printf("%s✓%s %d skills (unchanged)\n", ansiGreen, ansiReset, skillCount)
			return
		}
	}

	store := skills.Store()
	res, err := skills.ReconcileSkillDir(targetAgents, matched, store)
	if err != nil {
		die(err.Error())
	}
	totalAdded, totalRemoved := res.Added, res.Removed

	if projectDir != "" {
		ensureClaudeSkillsSymlink(filepath.Join(projectDir, ".claude", "skills"), targetAgents)
		// Clear the global dirs when using project-local (prevent stale syms).
		globalAgents := filepath.Join(home, ".agents", "skills")
		_, _ = skills.ReconcileSkillDir(globalAgents, nil, store)
		ensureClaudeSkillsSymlink(filepath.Join(home, ".claude", "skills"), globalAgents)
		ensureProjectGitignore(projectDir)
	} else {
		ensureClaudeSkillsSymlink(filepath.Join(home, ".claude", "skills"), targetAgents)
	}

	_ = os.MkdirAll(filepath.Dir(fingerprintFile), 0o755)
	_ = os.WriteFile(fingerprintFile, []byte(newFP+"\n"), 0o644)

	changeDesc := ""
	switch {
	case totalAdded > 0 && totalRemoved > 0:
		changeDesc = fmt.Sprintf(" (+%d added, -%d removed)", totalAdded, totalRemoved)
	case totalAdded > 0:
		changeDesc = fmt.Sprintf(" (+%d added)", totalAdded)
	case totalRemoved > 0:
		changeDesc = fmt.Sprintf(" (-%d removed)", totalRemoved)
	}
	fmt.Printf("%s✓%s %d skills%s\n", ansiGreen, ansiReset, skillCount, changeDesc)
}

// resolveMatchedWithSummary returns sorted matched skill dirnames and a
// "prefix: count, prefix: count" summary mirroring bash's python block.
func resolveMatchedWithSummary(patterns []string) ([]string, string) {
	if len(patterns) == 0 {
		return nil, ""
	}
	items, err := skills.Scan()
	if err != nil {
		return nil, ""
	}
	matchedDirs := map[string]string{} // dirname -> prefix
	for _, it := range items {
		id := it.SkillID()
		for _, p := range patterns {
			if ok, _ := filepath.Match(p, id); ok {
				matchedDirs[it.DirName] = it.Prefix
				break
			}
		}
	}
	dirs := make([]string, 0, len(matchedDirs))
	counts := map[string]int{}
	for d, p := range matchedDirs {
		dirs = append(dirs, d)
		counts[p]++
	}
	sort.Strings(dirs)

	prefixes := make([]string, 0, len(counts))
	for p := range counts {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	parts := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		parts = append(parts, fmt.Sprintf("%s: %d", p, counts[p]))
	}
	return dirs, strings.Join(parts, ", ")
}

// activateSyncRepos is a lightweight version of bash _activate_sync_repos.
// Bash has a 60-second mtime cache gate on .last-scan + a fetch/pull loop;
// our native cmdScan has the same cache gate, so we just call it. Output is
// suppressed to keep activate's stdout focused on the activation summary.
func activateSyncRepos() {
	// Redirect stdout of cmdScan by temporarily swapping os.Stdout.
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		cmdScan(nil)
		return
	}
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()
	defer func() {
		_ = w.Close()
		<-done
		os.Stdout = orig
	}()
	cmdScan(nil)
}

// ensureProjectGitignore is the Go port of bash _ensure_gitignore. Appends
// managed entries to <project>/.gitignore (idempotently) when the project
// has a .git directory. The ".agents/SOURCES.md" entry is preserved because
// historical installs may still have the file on disk, even though Go no
// longer creates one.
func ensureProjectGitignore(projectDir string) {
	if info, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil || !info.IsDir() {
		return
	}
	gitignore := filepath.Join(projectDir, ".gitignore")
	entries := []string{
		".claude/skills/",
		".agents/skills/",
		".agents/SOURCES.md",
		".claude/.ai-env-fingerprint",
	}

	var body []byte
	if b, err := os.ReadFile(gitignore); err == nil {
		body = b
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		existing[line] = true
	}

	needsUpdate := false
	for _, e := range entries {
		if !existing[e] {
			needsUpdate = true
			break
		}
	}
	if !needsUpdate {
		return
	}

	var out strings.Builder
	out.Write(body)
	// Mirror bash: ensure trailing newline before appending.
	if len(body) > 0 && body[len(body)-1] != '\n' {
		out.WriteByte('\n')
	}
	out.WriteByte('\n')
	out.WriteString("# ai-env managed skill directories\n")
	for _, e := range entries {
		if !existing[e] {
			out.WriteString(e)
			out.WriteByte('\n')
			existing[e] = true
		}
	}
	_ = os.WriteFile(gitignore, []byte(out.String()), 0o644)
}
